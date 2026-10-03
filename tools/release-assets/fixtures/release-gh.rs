// SPDX-License-Identifier: MIT OR Apache-2.0

use std::{env, fs, io::Write, path::{Path, PathBuf}, process::{Command, ExitCode}};

fn journal(root: &Path, name: &str, entry: &str) -> std::io::Result<()> {
    writeln!(fs::OpenOptions::new().append(true).open(root.join(name))?, "{entry}")
}

fn run() -> Result<bool, Box<dyn std::error::Error>> {
    let root = PathBuf::from(env::var_os("RELEASE_TEST_STATE").ok_or("missing fixture state")?);
    let args: Vec<String> = env::args().skip(1).collect();
    let executable = env::current_exe()?;
    if executable.file_stem() == Some(std::ffi::OsStr::new("cosign")) {
        let operation = args.first().ok_or("missing signing operation")?;
        let bundle = args.windows(2).find(|pair| pair[0] == "--bundle").ok_or("missing bundle")?;
        let subject = args.last().ok_or("missing subject")?;
        let bytes = fs::read(subject)?;
        let expected = [b"signed:".as_slice(), bytes.as_slice()].concat();
        journal(&root, "requests", operation)?;
        return match operation.as_str() {
            "sign-blob" => { fs::write(&bundle[1], expected)?; Ok(true) }
            "verify-blob" => Ok(fs::read(&bundle[1])? == expected
                && args.windows(2).any(|pair| pair == ["--certificate-oidc-issuer", "https://token.actions.githubusercontent.com"])
                && args.windows(2).any(|pair| pair[0] == "--certificate-identity-regexp" && pair[1].contains("release-publish[.]yml"))),
            _ => Err("unsupported signing operation".into()),
        };
    }
    if matches!(executable.file_stem().and_then(|name| name.to_str()), Some("cargo" | "mise")) {
        let index = args.iter().rposition(|arg| arg == "--").ok_or("missing controlled command separator")?;
        if !matches!(args.get(index + 1).map(String::as_str), Some("upload-draft" | "prepare-evidence" | "verify-attestation")) {
            return Err("unsupported fixture tool command".into());
        }
        return Ok(Command::new(env::var_os("RELEASE_XTASK_BINARY").ok_or("missing fixture binary")?)
            .args(&args[index + 1..]).status()?.success());
    }
    let state = fs::read_to_string(root.join("state"))?;
    if args.starts_with(&["attestation".into(), "verify".into()]) {
        journal(&root, "requests", "verify-attestation")?;
        let bundle = args.windows(2).find(|pair| pair[0] == "--bundle").ok_or("missing attestation bundle")?;
        return Ok(fs::read(&bundle[1])? == b"provenance"
            && args.windows(2).any(|pair| pair == ["--signer-workflow", "P4suta/release-fixture/.github/workflows/release-publish.yml"])
            && args.iter().any(|arg| arg == "--deny-self-hosted-runners"));
    }
    if args.first().is_some_and(|arg| arg == "api") {
        if state == "unavailable" || root.join("metadata-unavailable").exists() {
            return Ok(false);
        }
        let endpoint = args.iter().find(|arg| arg.starts_with("repos/")).ok_or("missing API endpoint")?;
        if endpoint.ends_with("/releases/tags/v0.1.0") {
            journal(&root, "requests", "metadata")?;
            let mut records = Vec::new();
            for (index, name) in ["fixture.zip", "checksums.txt", "fixture.zip.sigstore.json", "checksums.txt.sigstore.json", "github-attestation.sigstore.json"].iter().enumerate() {
                if root.join("assets").join(name).is_file() {
                    let recorded = root.join(format!("{name}.json"));
                    records.push(if recorded.exists() { fs::read_to_string(recorded)? } else {
                        format!("{{\"id\":{},\"name\":\"{name}\",\"size\":{},\"state\":\"uploaded\",\"digest\":null}}", index + 1, fs::metadata(root.join("assets").join(name))?.len())
                    });
                }
            }
            println!("{{\"draft\":{},\"tag_name\":\"v0.1.0\",\"assets\":[{}]}}",
                state.starts_with("draft"), records.join(","));
            return Ok(true);
        }
        let id: usize = endpoint.rsplit('/').next().ok_or("missing asset ID")?.parse()?;
        let names = ["fixture.zip", "checksums.txt", "fixture.zip.sigstore.json", "checksums.txt.sigstore.json", "github-attestation.sigstore.json"];
        let name = names.get(id.checked_sub(1).ok_or("zero asset ID")?).ok_or("unknown asset ID")?;
        journal(&root, "requests", &format!("download:{id}"))?;
        std::io::stdout().write_all(&fs::read(root.join("assets").join(name))?)?;
        return Ok(true);
    }
    let [release, operation, _rest @ ..] = args.as_slice() else {
        return Err("missing fixture command".into());
    };
    if release != "release" {
        return Err("unsupported fixture command".into());
    }
    journal(&root, "requests", operation)?;
    if state == "unavailable" {
        return Ok(false);
    }
    match operation.as_str() {
        "view" => {
            if state == "missing" { return Ok(false); }
            if args.iter().any(|arg| arg == "--json") {
                println!("{{\"isDraft\":{}}}", state.starts_with("draft"));
            }
            Ok(true)
        }
        "create" | "upload" => {
            if (operation == "create" && state != "missing") || state == "published" {
                return Ok(false);
            }
            if args.iter().any(|arg| arg == "--clobber") {
                return Err("asset replacement is forbidden".into());
            }
            let files: Vec<_> = args.iter().map(Path::new).filter(|path| path.is_file()).collect();
            if files.is_empty() { return Ok(false); }
            if operation == "create" && (!args.iter().any(|arg| arg == "--draft") || files.len() != 2) {
                return Ok(false);
            }
            for path in files {
                let name = path.file_name().ok_or("asset name")?.to_str().ok_or("UTF-8 asset name")?;
                let target = root.join("assets").join(name);
                if target.exists() { return Ok(false); }
                let fail = root.join("fail-once");
                if fail.exists() && fs::read_to_string(&fail)? == name {
                    fs::remove_file(fail)?;
                    return Ok(false);
                }
                fs::copy(path, target)?;
                if operation == "upload" {
                    journal(&root, "effects", &format!("upload:{name}"))?;
                }
            }
            if operation == "create" {
                journal(&root, "effects", "create-draft")?;
                fs::write(root.join("state"), "draft")?;
            }
            Ok(true)
        }
        "edit" if state.starts_with("draft") => {
            let verifies_tag = args.iter().any(|arg| arg == "--verify-tag")
                && args.windows(2).any(|pair| pair == ["--tag", "v0.1.0"]);
            if verifies_tag && state == "draft-tag-missing" { return Ok(false); }
            if args.iter().any(|arg| arg == "--draft=false") {
                journal(&root, "effects", "publish")?;
                fs::write(root.join("state"), "published")?;
                return Ok(true);
            }
            Ok(false)
        }
        _ => Err("unsupported fixture transition".into()),
    }
}

fn main() -> ExitCode {
    match run() {
        Ok(true) => ExitCode::SUCCESS,
        Ok(false) => ExitCode::FAILURE,
        Err(error) => { eprintln!("fixture: {error}"); ExitCode::from(2) }
    }
}
