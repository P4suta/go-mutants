// SPDX-License-Identifier: MIT OR Apache-2.0

use std::{
    collections::BTreeMap,
    fs::{self, File},
    io::Read,
    num::NonZeroU64,
    path::{Path, PathBuf},
    process::{Command, Stdio},
};

use anyhow::{Context as _, Result, ensure};
use serde::Deserialize;
use sha2::{Digest, Sha256};

#[derive(Clone, Debug, PartialEq, Eq)]
struct Fingerprint {
    size: u64,
    digest: [u8; 32],
}

impl Fingerprint {
    fn read(mut reader: impl Read) -> Result<Self> {
        let mut hasher = Sha256::new();
        let mut size = 0_u64;
        let mut buffer = vec![0_u8; 65_536].into_boxed_slice();
        loop {
            let count = reader.read(&mut buffer)?;
            if count == 0 {
                break;
            }
            size = size
                .checked_add(u64::try_from(count)?)
                .context("asset size overflow")?;
            hasher.update(&buffer[..count]);
        }
        Ok(Self {
            size,
            digest: hasher.finalize().into(),
        })
    }
}

#[derive(Deserialize)]
struct Release {
    draft: bool,
    tag_name: String,
    assets: Vec<Asset>,
}

#[derive(Deserialize)]
struct Asset {
    id: NonZeroU64,
    name: String,
    size: u64,
    digest: Option<String>,
    state: String,
}

struct LocalAsset {
    path: PathBuf,
    fingerprint: Fingerprint,
}

fn safe_component(value: &str) -> bool {
    !value.is_empty()
        && !value.starts_with('.')
        && !value.starts_with('-')
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._-+".contains(&byte))
}

fn metadata(repo: &str, tag: &str) -> Result<Release> {
    let output = Command::new("gh")
        .args([
            "api",
            "--method",
            "GET",
            &format!("repos/{repo}/releases/tags/{tag}"),
        ])
        .output()
        .context("read draft metadata")?;
    ensure!(
        output.status.success(),
        "draft metadata request failed: {}",
        output.status
    );
    let release: Release =
        serde_json::from_slice(&output.stdout).context("invalid draft metadata")?;
    ensure!(
        release.draft && release.tag_name == tag,
        "the exact target must be an unpublished draft"
    );
    Ok(release)
}

fn downloaded_fingerprint(repo: &str, asset: &Asset) -> Result<Fingerprint> {
    let mut child = Command::new("gh")
        .args([
            "api",
            "--method",
            "GET",
            &format!("repos/{repo}/releases/assets/{}", asset.id),
            "--header",
            "Accept: application/octet-stream",
        ])
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .context("download legacy draft asset")?;
    let reader = child
        .stdout
        .take()
        .context("asset download requires stdout")?;
    read_download(child, reader)
}

fn missing<'a>(
    release: &Release,
    local: &'a BTreeMap<String, LocalAsset>,
    repo: &str,
    allow_other_assets: bool,
) -> Result<Vec<&'a LocalAsset>> {
    let mut remaining: BTreeMap<_, _> = local
        .iter()
        .map(|(name, asset)| (name.as_str(), asset))
        .collect();
    let mut names = std::collections::BTreeSet::new();
    for asset in &release.assets {
        ensure!(safe_component(&asset.name), "invalid remote asset name");
        ensure!(names.insert(&asset.name), "duplicate remote asset name");
        ensure!(asset.state == "uploaded", "remote asset is incomplete");
        let Some(expected) = remaining.remove(asset.name.as_str()) else {
            ensure!(allow_other_assets, "unexpected remote asset");
            continue;
        };
        ensure!(
            asset.size == expected.fingerprint.size,
            "asset size mismatch: {}",
            asset.name
        );
        let actual = match recorded_fingerprint(asset)? {
            Some(recorded) => recorded,
            None => downloaded_fingerprint(repo, asset)?,
        };
        ensure!(
            actual == expected.fingerprint,
            "asset digest mismatch: {}",
            asset.name
        );
    }
    Ok(remaining.into_values().collect())
}

fn validate_scope(repo: &str, tag: &str) -> Result<()> {
    let components: Vec<_> = repo.split('/').collect();
    ensure!(
        components.len() == 2 && components.iter().all(|part| safe_component(part)),
        "expected owner/repository"
    );
    ensure!(safe_component(tag), "invalid release tag");
    Ok(())
}

fn local_assets(
    directory: Option<&Path>,
    files: &[PathBuf],
) -> Result<BTreeMap<String, LocalAsset>> {
    let mut paths = files.to_vec();
    if let Some(directory) = directory {
        for entry in fs::read_dir(directory).context("read local artifacts")? {
            paths.push(entry?.path());
        }
    }
    ensure!(!paths.is_empty(), "at least one artifact is required");
    let mut local = BTreeMap::new();
    for path in paths {
        let metadata = fs::symlink_metadata(&path)?;
        ensure!(
            metadata.is_file() && !metadata.file_type().is_symlink(),
            "artifact must be a regular file"
        );
        let name = path
            .file_name()
            .and_then(|name| name.to_str())
            .context("UTF-8 artifact name required")?;
        ensure!(safe_component(name), "invalid artifact name");
        let asset = LocalAsset {
            path: fs::canonicalize(&path)?,
            fingerprint: Fingerprint::read(File::open(&path)?)?,
        };
        ensure!(
            local.insert(name.to_owned(), asset).is_none(),
            "duplicate local artifact name"
        );
    }
    Ok(local)
}

pub fn upload_draft(
    repo: &str,
    tag: &str,
    directory: Option<&Path>,
    files: &[PathBuf],
    allow_other_assets: bool,
) -> Result<()> {
    validate_scope(repo, tag)?;
    let local = local_assets(directory, files)?;
    let release = metadata(repo, tag)?;
    let plan = missing(&release, &local, repo, allow_other_assets)?;
    for asset in plan {
        let status = Command::new("gh")
            .args(["release", "upload", tag])
            .arg(&asset.path)
            .args(["--repo", repo])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .context("upload missing draft asset")?;
        ensure!(status.success(), "draft asset upload failed: {status}");
    }
    let verified = metadata(repo, tag)?;
    ensure!(
        missing(&verified, &local, repo, allow_other_assets)?.is_empty(),
        "draft asset set remains incomplete"
    );
    Ok(())
}

fn recorded_fingerprint(asset: &Asset) -> Result<Option<Fingerprint>> {
    asset
        .digest
        .as_ref()
        .map(|digest| {
            let encoded = digest
                .strip_prefix("sha256:")
                .context("unsupported remote digest algorithm")?;
            let bytes = hex::decode(encoded).context("invalid remote SHA-256")?;
            Ok(Fingerprint {
                size: asset.size,
                digest: bytes
                    .try_into()
                    .map_err(|_| anyhow::anyhow!("invalid remote SHA-256 length"))?,
            })
        })
        .transpose()
}

pub fn distribution_files(directory: &Path) -> Result<Vec<PathBuf>> {
    let mut files = Vec::new();
    for entry in fs::read_dir(directory)? {
        let path = entry?.path();
        let name = path
            .file_name()
            .and_then(|value| value.to_str())
            .context("UTF-8 artifact name")?;
        if path
            .extension()
            .is_some_and(|extension| extension.eq_ignore_ascii_case("zip"))
            || name.ends_with(".tar.gz")
            || name == "checksums.txt"
        {
            files.push(path);
        }
    }
    ensure!(
        files
            .iter()
            .any(|path| path.file_name().is_some_and(|name| name == "checksums.txt")),
        "distribution requires checksums"
    );
    ensure!(files.len() > 1, "distribution requires archives");
    files.sort();
    Ok(files)
}

fn identity_policy(repo: &str, tag: &str) -> String {
    let escape = |value: &str| value.replace('.', "[.]").replace('+', "[+]");
    format!(
        "^https://github[.]com/{}/[.]github/workflows/release-publish[.]yml@refs/(heads/main|tags/{})$",
        escape(repo),
        escape(tag)
    )
}

fn restore_bundle(repo: &str, asset: &Asset, destination: &Path) -> Result<()> {
    ensure!(
        asset.size <= 16 * 1024 * 1024,
        "evidence bundle exceeds the supported size"
    );
    let bytes = if destination.exists() {
        let metadata = fs::symlink_metadata(destination)?;
        ensure!(
            metadata.is_file() && !metadata.file_type().is_symlink(),
            "evidence must be a regular file"
        );
        fs::read(destination)?
    } else {
        let output = Command::new("gh")
            .args([
                "api",
                "--method",
                "GET",
                &format!("repos/{repo}/releases/assets/{}", asset.id),
                "--header",
                "Accept: application/octet-stream",
            ])
            .output()
            .context("download preserved evidence")?;
        ensure!(
            output.status.success(),
            "evidence download failed: {}",
            output.status
        );
        output.stdout
    };
    let actual = Fingerprint::read(bytes.as_slice())?;
    ensure!(actual.size == asset.size, "evidence size mismatch");
    if let Some(expected) = recorded_fingerprint(asset)? {
        ensure!(actual == expected, "evidence digest mismatch");
    }
    if !destination.exists() {
        let mut file = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(destination)?;
        std::io::Write::write_all(&mut file, &bytes)?;
        file.sync_all()?;
    }
    Ok(())
}

fn success(command: &mut Command, operation: &'static str) -> Result<()> {
    let status = command.status().context(operation)?;
    ensure!(status.success(), "{operation} failed: {status}");
    Ok(())
}

pub fn prepare_evidence(repo: &str, tag: &str, directory: &Path, output: &Path) -> Result<()> {
    validate_scope(repo, tag)?;
    let local = local_assets(None, &distribution_files(directory)?)?;
    let release = metadata(repo, tag)?;
    ensure!(
        missing(&release, &local, repo, true)?.is_empty(),
        "distribution must be uploaded and verified before signing"
    );
    for (name, asset) in &local {
        let bundle_name = format!("{name}.sigstore.json");
        let bundle = directory.join(&bundle_name);
        if let Some(existing) = release
            .assets
            .iter()
            .find(|asset| asset.name == bundle_name)
        {
            restore_bundle(repo, existing, &bundle)?;
        } else if !bundle.exists() {
            success(
                Command::new("cosign")
                    .args(["sign-blob", "--yes", "--bundle"])
                    .arg(&bundle)
                    .arg(&asset.path),
                "sign distribution artifact",
            )?;
        }
        success(
            Command::new("cosign")
                .args(["verify-blob", "--bundle"])
                .arg(&bundle)
                .args([
                    "--certificate-oidc-issuer",
                    "https://token.actions.githubusercontent.com",
                    "--certificate-identity-regexp",
                    &identity_policy(repo, tag),
                ])
                .arg(&asset.path),
            "verify distribution signature",
        )?;
    }
    let existing = release
        .assets
        .iter()
        .find(|asset| asset.name == "github-attestation.sigstore.json");
    if let Some(existing) = existing {
        restore_bundle(
            repo,
            existing,
            &directory.join("github-attestation.sigstore.json"),
        )?;
        verify_attestation(repo, tag, directory)?;
    }
    let mut file = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(output)?;
    std::io::Write::write_all(
        &mut file,
        format!("attestation_reused={}\n", existing.is_some()).as_bytes(),
    )?;
    Ok(())
}

pub fn verify_attestation(repo: &str, tag: &str, directory: &Path) -> Result<()> {
    validate_scope(repo, tag)?;
    let files = distribution_files(directory)?;
    let bundle = directory.join("github-attestation.sigstore.json");
    let _validated = local_assets(None, std::slice::from_ref(&bundle))?;
    for subject in files {
        if subject
            .file_name()
            .is_some_and(|name| name == "checksums.txt")
        {
            continue;
        }
        success(
            Command::new("gh")
                .args(["attestation", "verify"])
                .arg(subject)
                .arg("--bundle")
                .arg(&bundle)
                .args([
                    "--repo",
                    repo,
                    "--signer-workflow",
                    &format!("{repo}/.github/workflows/release-publish.yml"),
                    "--cert-identity-regex",
                    &identity_policy(repo, tag),
                    "--deny-self-hosted-runners",
                ]),
            "verify distribution provenance",
        )?;
    }
    Ok(())
}

fn read_download(mut child: std::process::Child, reader: impl Read) -> Result<Fingerprint> {
    let fingerprint = match Fingerprint::read(reader) {
        Ok(fingerprint) => fingerprint,
        Err(error) => {
            let _kill_result = child.kill();
            let _wait_result = child.wait();
            return Err(error);
        }
    };
    let status = child.wait().context("wait for asset download")?;
    ensure!(status.success(), "asset download failed: {status}");
    Ok(fingerprint)
}

#[cfg(test)]
mod tests {
    use super::*;

    struct FailingReader;

    impl Read for FailingReader {
        fn read(&mut self, _buffer: &mut [u8]) -> std::io::Result<usize> {
            Err(std::io::Error::new(
                std::io::ErrorKind::PermissionDenied,
                "controlled primary read failure",
            ))
        }
    }

    #[test]
    fn download_preserves_read_failure_when_child_is_terminated_or_already_failed() {
        for argument in ["--version", "--not-a-rustc-option"] {
            let mut child = Command::new("rustc")
                .arg(argument)
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()
                .expect("controlled compiler child");
            if argument == "--not-a-rustc-option" {
                assert!(
                    !child.wait().expect("controlled child completion").success(),
                    "the controlled child must already have failed"
                );
            }
            let error = read_download(child, FailingReader).expect_err("controlled stream failure");
            assert_eq!(
                error.to_string(),
                "controlled primary read failure",
                "cleanup must preserve the causal read error"
            );
        }
    }
}
