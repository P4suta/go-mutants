// SPDX-License-Identifier: MIT OR Apache-2.0

use anyhow::Result;
use clap::{Parser, Subcommand};
use std::{path::PathBuf, process::ExitCode};

mod release;

#[derive(Parser)]
#[command(
    version,
    about = "Reconcile immutable release drafts without publication"
)]
struct Cli {
    #[command(subcommand)]
    command: Operation,
}

#[derive(Subcommand)]
enum Operation {
    #[command(about = "Verify existing draft assets and upload only missing files")]
    UploadDraft {
        #[arg(long)]
        repo: String,
        #[arg(long)]
        tag: String,
        #[arg(long, conflicts_with = "asset")]
        artifacts: Option<PathBuf>,
        #[arg(long, num_args = 1..)]
        asset: Vec<PathBuf>,
        #[arg(long)]
        allow_other_assets: bool,
        #[arg(
            long,
            requires = "artifacts",
            help = "Select distribution archives and checksums"
        )]
        distribution_only: bool,
    },
    #[command(about = "Restore and verify preserved evidence, signing only missing signatures")]
    PrepareEvidence {
        #[arg(long)]
        repo: String,
        #[arg(long)]
        tag: String,
        #[arg(long)]
        artifacts: PathBuf,
        #[arg(long)]
        output: PathBuf,
    },
    #[command(about = "Verify exact distribution provenance and signer workflow identity")]
    VerifyAttestation {
        #[arg(long)]
        repo: String,
        #[arg(long)]
        tag: String,
        #[arg(long)]
        artifacts: PathBuf,
    },
}

fn run(cli: Cli) -> Result<()> {
    match cli.command {
        Operation::UploadDraft {
            repo,
            tag,
            artifacts,
            mut asset,
            allow_other_assets,
            distribution_only,
        } => {
            if distribution_only {
                asset.extend(release::distribution_files(
                    artifacts
                        .as_deref()
                        .ok_or_else(|| anyhow::anyhow!("distribution requires a directory"))?,
                )?);
            }
            release::upload_draft(
                &repo,
                &tag,
                if distribution_only {
                    None
                } else {
                    artifacts.as_deref()
                },
                &asset,
                allow_other_assets,
            )
        }
        Operation::PrepareEvidence {
            repo,
            tag,
            artifacts,
            output,
        } => release::prepare_evidence(&repo, &tag, &artifacts, &output),
        Operation::VerifyAttestation {
            repo,
            tag,
            artifacts,
        } => release::verify_attestation(&repo, &tag, &artifacts),
    }
}

fn main() -> ExitCode {
    match run(Cli::parse()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("release-assets: {error:#}");
            ExitCode::FAILURE
        }
    }
}
