// SPDX-License-Identifier: MIT OR Apache-2.0

use std::{collections::BTreeMap, fs, path::PathBuf, process::Command, sync::OnceLock};

use serde::Deserialize;
use serde_json::json;
use sha2::{Digest, Sha256};
use uuid::Uuid;

#[derive(Deserialize)]
struct Workflow {
    concurrency: Concurrency,
    jobs: BTreeMap<String, Job>,
}

#[derive(Deserialize)]
struct Concurrency {
    group: String,
    #[serde(rename = "cancel-in-progress")]
    cancel_in_progress: bool,
}

#[derive(Deserialize)]
struct Environment {
    name: String,
}

#[derive(Deserialize)]
struct Job {
    environment: Environment,
    #[serde(default)]
    steps: Vec<Step>,
}

#[derive(Deserialize)]
struct Step {
    uses: Option<String>,
    #[serde(rename = "with", default)]
    parameters: BTreeMap<String, serde_json::Value>,
    name: Option<String>,
    run: Option<String>,
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn fixture_binary() -> &'static PathBuf {
    static FIXTURE: OnceLock<PathBuf> = OnceLock::new();
    FIXTURE.get_or_init(|| {
        let output = root()
            .join("tools/release-assets/target/verification/release-workflow")
            .join(Uuid::new_v4().to_string())
            .join("bin");
        fs::create_dir_all(&output).expect("controlled fixture binary directory");
        let binary = output.join(if cfg!(windows) { "gh.exe" } else { "gh" });
        let status = Command::new("rustc")
            .arg("--edition=2024")
            .arg("--crate-name=release_gh_fixture")
            .arg(root().join("tools/release-assets/fixtures/release-gh.rs"))
            .arg("-o")
            .arg(&binary)
            .status()
            .expect("compile the Rust API fixture");
        assert!(status.success(), "the controlled fixture must compile");
        fs::copy(
            &binary,
            output.join(if cfg!(windows) { "cargo.exe" } else { "cargo" }),
        )
        .expect("controlled cargo adapter");
        fs::copy(
            &binary,
            output.join(if cfg!(windows) { "mise.exe" } else { "mise" }),
        )
        .expect("controlled mise adapter");
        fs::copy(
            &binary,
            output.join(if cfg!(windows) {
                "cosign.exe"
            } else {
                "cosign"
            }),
        )
        .expect("controlled signing fixture");
        binary
    })
}

fn bash() -> PathBuf {
    let mut candidates = Vec::new();
    if cfg!(windows) {
        if let Some(directory) = std::env::var_os("ProgramFiles") {
            candidates.push(PathBuf::from(directory).join("Git/bin/bash.exe"));
        }
        for directory in std::env::split_paths(&std::env::var_os("PATH").expect("tool PATH")) {
            if directory.join("git.exe").is_file()
                && let Some(parent) = directory.parent()
            {
                candidates.push(parent.join("bin/bash.exe"));
            }
        }
    }
    candidates.push(PathBuf::from("bash"));
    candidates
        .into_iter()
        .find(|program| {
            Command::new(program)
                .arg("--version")
                .output()
                .is_ok_and(|output| {
                    output.status.success() && output.stdout.starts_with(b"GNU bash, version")
                })
        })
        .expect("the workflow requires GNU Bash; install Git for Windows on Windows")
}

struct Fixture {
    workspace: PathBuf,
    body: String,
}

impl Fixture {
    fn new(state: &str) -> Self {
        let workflow: Workflow = serde_saphyr::from_str(include_str!(
            "../../../.github/workflows/release-publish.yml"
        ))
        .expect("parse the production workflow");
        let body = workflow.jobs["publish"]
            .steps
            .iter()
            .filter(|step| {
                matches!(
                    step.name.as_deref(),
                    Some(
                        "Require an unpublished draft"
                            | "Upload verified distribution files"
                            | "Publish the complete draft"
                    )
                )
            })
            .filter_map(|step| step.run.as_deref())
            .collect::<Vec<_>>()
            .join("\n");
        let workspace = root()
            .join("tools/release-assets/target/verification/release-workflow")
            .join(Uuid::new_v4().to_string());
        fs::create_dir_all(workspace.join("dist")).expect("controlled artifact directory");
        fs::write(workspace.join("dist/fixture.zip"), "verified archive")
            .expect("controlled archive");
        fs::write(workspace.join("dist/checksums.txt"), "controlled checksum")
            .expect("controlled checksum file");
        fs::write(workspace.join("state"), state).expect("controlled API state");
        fs::write(workspace.join("effects"), "").expect("controlled API effect journal");
        fs::write(workspace.join("requests"), "").expect("controlled API request journal");
        fs::create_dir(workspace.join("assets")).expect("controlled remote assets");
        for (index, name) in ["fixture.zip", "checksums.txt"].iter().enumerate() {
            let bytes = fs::read(workspace.join("dist").join(name)).expect("fixture bytes");
            fs::write(workspace.join(format!("{name}.json")), serde_json::to_vec(&json!({
            "id": index + 1, "name": name, "size": bytes.len(),
            "state": "uploaded", "digest": format!("sha256:{}", hex::encode(Sha256::digest(&bytes)))
        })).expect("fixture metadata")).expect("controlled asset metadata");
        }
        Self { workspace, body }
    }

    fn existing(&self, name: &str) {
        fs::copy(
            self.workspace.join("dist").join(name),
            self.workspace.join("assets").join(name),
        )
        .expect("preserved remote asset");
    }

    fn execute(&self) -> (bool, String, String) {
        let workspace = &self.workspace;
        let mut paths = vec![
            fixture_binary()
                .parent()
                .expect("fixture parent")
                .to_path_buf(),
        ];
        paths.extend(std::env::split_paths(
            &std::env::var_os("PATH").expect("tool PATH"),
        ));
        let mut shell = Command::new(bash());
        shell.env_clear();
        for name in ["SystemRoot", "COMSPEC", "TEMP", "TMP"] {
            if let Some(value) = std::env::var_os(name) {
                shell.env(name, value);
            }
        }
        let result = shell
            .current_dir(workspace)
            .env(
                "PATH",
                std::env::join_paths(paths).expect("controlled fixture PATH"),
            )
            .env("RELEASE_TEST_STATE", workspace)
            .env("RELEASE_XTASK_BINARY", env!("CARGO_BIN_EXE_release-assets"))
            .env("GITHUB_REPOSITORY", "P4suta/release-fixture")
            .env("RELEASE_TAG", "v0.1.0")
            .args(["--noprofile", "--norc", "-c", &self.body])
            .output()
            .expect("execute the production algorithm against the Rust API fixture");
        let attempt = Uuid::new_v4();
        fs::write(workspace.join(format!("{attempt}.stdout")), result.stdout)
            .expect("fixture stdout evidence");
        fs::write(workspace.join(format!("{attempt}.stderr")), result.stderr)
            .expect("fixture stderr evidence");
        let requests =
            fs::read_to_string(workspace.join("requests")).expect("fixture request evidence");
        assert!(
            requests.starts_with("view\n")
                || requests.starts_with("metadata\n")
                || requests.starts_with("verify-attestation\n"),
            "the production workflow must reach the Rust API fixture; evidence: {}",
            workspace.display()
        );
        (
            result.status.success(),
            fs::read_to_string(workspace.join("state")).expect("final fixture state"),
            fs::read_to_string(workspace.join("effects")).expect("final fixture effect journal"),
        )
    }
}

fn execute(state: &str) -> (bool, String, String) {
    Fixture::new(state).execute()
}

#[test]
fn missing_target_requires_an_existing_release_please_draft() {
    let (passed, state, effects) = execute("missing");
    assert!(!passed, "release-please must supply the draft");
    assert_eq!(state, "missing", "no draft is guessed");
    assert_eq!(
        effects, "",
        "this command never creates or publishes a target"
    );
}

#[test]
fn existing_draft_is_completed_before_publication() {
    let (passed, state, effects) = execute("draft");
    assert!(passed, "an unpublished target must complete");
    assert_eq!(state, "published", "the complete draft is finalized");
    assert_eq!(
        effects, "upload:checksums.txt\nupload:fixture.zip\npublish\n",
        "upload precedes publication"
    );
}

#[test]
fn publication_preserves_approval_tag_identity_and_exclusive_authority() {
    let workflow: Workflow = serde_saphyr::from_str(include_str!(
        "../../../.github/workflows/release-publish.yml"
    ))
    .expect("parse the production workflow");
    let publisher = &workflow.jobs["publish"];
    assert_eq!(
        publisher.environment.name, "release",
        "publication must enter the protected approval environment"
    );
    assert!(
        !workflow.concurrency.group.is_empty() && !workflow.concurrency.group.contains("${{"),
        "new publication and tag-based retries must share one publication group"
    );
    assert!(
        !workflow.concurrency.cancel_in_progress,
        "a retry must not interrupt an active publication"
    );
    let checkout = publisher
        .steps
        .iter()
        .find(|step| {
            step.uses
                .as_deref()
                .is_some_and(|action| action.starts_with("actions/checkout@"))
        })
        .expect("source checkout");
    assert_eq!(
        checkout.parameters.get("ref"),
        Some(&json!("refs/tags/${{ steps.target.outputs.tag }}")),
        "only the validated tag can select the release source"
    );
    assert_eq!(
        checkout.parameters.get("persist-credentials"),
        Some(&json!(false)),
        "the publisher must not leave ambient Git write credentials"
    );
}

#[test]
fn published_targets_are_rejected_without_asset_mutation() {
    let (passed, state, effects) = execute("published");
    assert!(!passed, "a published target cannot be reused");
    assert_eq!(state, "published", "the published target is preserved");
    assert_eq!(effects, "", "the client must not modify published assets");
}

#[test]
fn unavailable_metadata_never_becomes_a_publication() {
    let (passed, state, effects) = execute("unavailable");
    assert!(!passed, "an unavailable target cannot be guessed");
    assert_eq!(state, "unavailable", "unknown remote state is preserved");
    assert_eq!(effects, "", "a failed API cannot finalize a release");
}

#[test]
fn matching_assets_are_reused_after_failed_publication() {
    let fixture = Fixture::new("draft");
    fixture.existing("fixture.zip");
    fixture.existing("checksums.txt");
    let (passed, state, effects) = fixture.execute();
    assert!(passed, "identical assets must allow retry completion");
    assert_eq!(state, "published", "the verified draft is finalized");
    assert_eq!(
        effects, "publish\n",
        "existing assets must never be replaced"
    );
}

#[test]
fn partial_upload_resumes_with_only_the_missing_asset() {
    let fixture = Fixture::new("draft");
    fixture.existing("fixture.zip");
    let (passed, _, effects) = fixture.execute();
    assert!(passed, "the missing checksum must be added");
    assert_eq!(
        effects, "upload:checksums.txt\npublish\n",
        "preserved archive is not uploaded twice"
    );
}

#[test]
fn conflicting_asset_aborts_before_any_upload() {
    let fixture = Fixture::new("draft");
    fixture.existing("checksums.txt");
    let path = fixture.workspace.join("checksums.txt.json");
    let mut metadata: serde_json::Value =
        serde_json::from_slice(&fs::read(&path).expect("metadata")).expect("valid metadata");
    metadata["digest"] = json!(format!(
        "sha256:{}",
        hex::encode(Sha256::digest(b"other bytes"))
    ));
    fs::write(path, serde_json::to_vec(&metadata).expect("metadata bytes"))
        .expect("mismatch metadata");
    let (passed, state, effects) = fixture.execute();
    assert!(!passed, "a digest mismatch must fail closed");
    assert_eq!(state, "draft", "the draft must remain unpublished");
    assert_eq!(effects, "", "all conflicts are checked before any upload");
}

#[test]
fn legacy_assets_are_verified_from_downloaded_bytes() {
    let fixture = Fixture::new("draft");
    fixture.existing("fixture.zip");
    let path = fixture.workspace.join("fixture.zip.json");
    let mut metadata: serde_json::Value =
        serde_json::from_slice(&fs::read(&path).expect("metadata")).expect("valid metadata");
    metadata["digest"] = serde_json::Value::Null;
    fs::write(path, serde_json::to_vec(&metadata).expect("metadata bytes"))
        .expect("legacy metadata");
    let (passed, _, effects) = fixture.execute();
    assert!(passed, "legacy assets require byte-level verification");
    assert_eq!(
        effects, "upload:checksums.txt\npublish\n",
        "legacy archive is preserved"
    );
    assert!(
        fs::read_to_string(fixture.workspace.join("requests"))
            .expect("request log")
            .contains("download:1\n"),
        "the legacy asset must be downloaded"
    );
}

#[test]
fn upload_failure_can_resume_without_replacing_the_successful_asset() {
    let fixture = Fixture::new("draft");
    fs::write(fixture.workspace.join("fail-once"), "fixture.zip").expect("injected failure");
    let (passed, state, effects) = fixture.execute();
    assert!(
        !passed,
        "the first partial upload must preserve its failure"
    );
    assert_eq!(state, "draft", "a partial draft is not published");
    assert_eq!(
        effects, "upload:checksums.txt\n",
        "the checksum upload is retained"
    );
    let (passed, _, effects) = fixture.execute();
    assert!(
        passed,
        "the same draft must resume after the controlled failure"
    );
    assert_eq!(
        effects, "upload:checksums.txt\nupload:fixture.zip\npublish\n",
        "each asset is uploaded once"
    );
}

#[test]
fn pending_tag_does_not_publish_an_existing_draft() {
    let (passed, state, effects) = execute("draft-tag-missing");
    assert!(!passed, "remote tag verification must reject a pending tag");
    assert_eq!(state, "draft-tag-missing", "the draft is preserved");
    assert!(!effects.contains("publish"), "publication must not happen");
}

#[test]
fn invalid_remote_assets_fail_before_upload_or_publication() {
    for (field, value) in [
        ("size", json!(0)),
        ("state", json!("starter")),
        ("digest", json!("sha512:0000")),
        ("digest", json!("sha256:00")),
        ("digest", json!("sha256:not-hexadecimal")),
        ("id", json!(0)),
        ("name", json!("unexpected.zip")),
    ] {
        let fixture = Fixture::new("draft");
        fixture.existing("checksums.txt");
        let path = fixture.workspace.join("checksums.txt.json");
        let mut metadata: serde_json::Value =
            serde_json::from_slice(&fs::read(&path).expect("metadata")).expect("valid metadata");
        metadata[field] = value;
        fs::write(path, serde_json::to_vec(&metadata).expect("metadata bytes"))
            .expect("invalid remote metadata");
        let (passed, state, effects) = fixture.execute();
        assert!(!passed, "invalid {field} must fail closed");
        assert_eq!(state, "draft", "invalid metadata cannot publish");
        assert_eq!(effects, "", "invalid {field} cannot upload anything");
    }
}

#[test]
fn asset_metadata_failure_preserves_an_existing_draft() {
    let fixture = Fixture::new("draft");
    fs::write(fixture.workspace.join("metadata-unavailable"), "").expect("injected API failure");
    let (passed, state, effects) = fixture.execute();
    assert!(!passed, "unavailable asset metadata must fail closed");
    assert_eq!(state, "draft", "the original draft is preserved");
    assert_eq!(effects, "", "unknown asset state cannot upload or publish");
}

#[test]
fn changed_legacy_bytes_cannot_be_reused() {
    let fixture = Fixture::new("draft");
    fixture.existing("fixture.zip");
    let path = fixture.workspace.join("fixture.zip.json");
    let mut metadata: serde_json::Value =
        serde_json::from_slice(&fs::read(&path).expect("metadata")).expect("valid metadata");
    metadata["digest"] = serde_json::Value::Null;
    fs::write(path, serde_json::to_vec(&metadata).expect("metadata bytes"))
        .expect("legacy metadata");
    fs::write(
        fixture.workspace.join("assets/fixture.zip"),
        "different bytes!",
    )
    .expect("conflicting legacy asset");
    let (passed, _, effects) = fixture.execute();
    assert!(!passed, "legacy assets must match their actual bytes");
    assert_eq!(
        effects, "",
        "a legacy mismatch is checked before any upload"
    );
}

fn prepare_fixture() -> Fixture {
    let mut fixture = Fixture::new("draft");
    fixture.existing("fixture.zip");
    fixture.existing("checksums.txt");
    fixture.body = "mise exec -- cargo run --locked --manifest-path tools/release-assets/Cargo.toml -- prepare-evidence --repo \"$GITHUB_REPOSITORY\" --tag \"$RELEASE_TAG\" --artifacts dist --output outputs".into();
    fixture
}

#[test]
fn existing_signatures_and_provenance_are_restored_without_resigning() {
    let fixture = prepare_fixture();
    for name in ["fixture.zip", "checksums.txt"] {
        let bytes = fs::read(fixture.workspace.join("dist").join(name)).expect("subject bytes");
        fs::write(
            fixture
                .workspace
                .join("assets")
                .join(format!("{name}.sigstore.json")),
            [b"signed:".as_slice(), bytes.as_slice()].concat(),
        )
        .expect("existing signature");
    }
    fs::write(
        fixture
            .workspace
            .join("assets/github-attestation.sigstore.json"),
        "provenance",
    )
    .expect("existing attestation");
    let (passed, _, effects) = fixture.execute();
    assert!(passed, "matching evidence must be restored");
    assert_eq!(
        effects, "",
        "restoring evidence cannot mutate remote assets"
    );
    let requests = fs::read_to_string(fixture.workspace.join("requests")).expect("request journal");
    assert!(
        !requests.contains("sign-blob\n"),
        "existing signatures must not be regenerated"
    );
    assert_eq!(
        requests.matches("verify-blob\n").count(),
        2,
        "every restored signature is verified"
    );
    assert_eq!(
        fs::read_to_string(fixture.workspace.join("outputs")).expect("outputs"),
        "attestation_reused=true\n",
        "a valid preserved attestation is reused"
    );
}

#[test]
fn missing_signatures_are_created_and_verified_without_publication() {
    let fixture = prepare_fixture();
    let (passed, _, effects) = fixture.execute();
    assert!(
        passed,
        "new signatures must be prepared through the controlled signer"
    );
    assert_eq!(effects, "", "signing preparation cannot publish or upload");
    let requests = fs::read_to_string(fixture.workspace.join("requests")).expect("request journal");
    assert_eq!(
        requests.matches("sign-blob\n").count(),
        2,
        "each missing signature is created"
    );
    assert_eq!(
        requests.matches("verify-blob\n").count(),
        2,
        "each signature is verified"
    );
    assert_eq!(
        fs::read_to_string(fixture.workspace.join("outputs")).expect("outputs"),
        "attestation_reused=false\n",
        "a missing attestation requires a new action"
    );
}

#[test]
fn corrupt_existing_signature_stops_evidence_preparation() {
    let fixture = prepare_fixture();
    fs::write(
        fixture.workspace.join("assets/checksums.txt.sigstore.json"),
        "invalid signature",
    )
    .expect("invalid bundle");
    let (passed, _, effects) = fixture.execute();
    assert!(
        !passed,
        "restored evidence must pass cryptographic verification"
    );
    assert_eq!(effects, "", "invalid evidence cannot mutate remote state");
    assert!(
        !fixture.workspace.join("outputs").exists(),
        "invalid evidence cannot authorize later stages"
    );
}

#[test]
fn attestation_verification_uses_the_expected_workflow_and_artifacts() {
    for valid in [true, false] {
        let mut fixture = prepare_fixture();
        fs::write(
            fixture
                .workspace
                .join("dist/github-attestation.sigstore.json"),
            if valid { "provenance" } else { "invalid" },
        )
        .expect("controlled provenance");
        fixture.body = "mise exec -- cargo run --locked --manifest-path tools/release-assets/Cargo.toml -- verify-attestation --repo \"$GITHUB_REPOSITORY\" --tag \"$RELEASE_TAG\" --artifacts dist".into();
        let (passed, _, effects) = fixture.execute();
        assert_eq!(passed, valid, "the actual verifier result is authoritative");
        assert_eq!(
            effects, "",
            "attestation verification cannot mutate release state"
        );
    }
}
