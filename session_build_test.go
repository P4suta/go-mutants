// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
)

func TestPreparationBuildsOverlapAndReturnBothProducts(t *testing.T) {
	mainStarted := make(chan struct{})
	probeStarted := make(chan struct{})
	mainProduct := mainBuildResult{options: executeOptionsWithPackages("./main")}
	probeProduct := probeBuildResult{options: executeOptionsWithPackages("./probe")}

	mainResult, probeResult, err := runPreparationBuilds(t.Context(),
		func(ctx context.Context) (mainBuildResult, error) {
			close(mainStarted)
			select {
			case <-probeStarted:
				return mainProduct, nil
			case <-ctx.Done():
				return mainBuildResult{}, ctx.Err()
			}
		},
		func(ctx context.Context) (probeBuildResult, error) {
			close(probeStarted)
			select {
			case <-mainStarted:
				return probeProduct, nil
			case <-ctx.Done():
				return probeBuildResult{}, ctx.Err()
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mainResult.options.Packages, mainProduct.options.Packages) {
		t.Fatalf("main packages = %v, want %v", mainResult.options.Packages, mainProduct.options.Packages)
	}
	if !slices.Equal(probeResult.options.Packages, probeProduct.options.Packages) {
		t.Fatalf("probe packages = %v, want %v", probeResult.options.Packages, probeProduct.options.Packages)
	}
}

func TestPreparationBuildsPreserveMainFailurePrecedence(t *testing.T) {
	mainFailure := errors.New("main build failed")
	probeFailure := errors.New("probe build failed")
	_, _, err := runPreparationBuilds(t.Context(),
		func(context.Context) (mainBuildResult, error) {
			return mainBuildResult{}, mainFailure
		},
		func(context.Context) (probeBuildResult, error) {
			return probeBuildResult{}, probeFailure
		},
	)
	if !errors.Is(err, mainFailure) {
		t.Fatalf("error = %v, want main failure", err)
	}
	if errors.Is(err, probeFailure) {
		t.Fatalf("error = %v, unexpectedly exposes the later probe failure", err)
	}
}

func TestPreparationBuildsCancelProbeAfterMainFailure(t *testing.T) {
	mainFailure := errors.New("main build failed")
	probeStopped := make(chan struct{})
	_, _, err := runPreparationBuilds(t.Context(),
		func(context.Context) (mainBuildResult, error) {
			return mainBuildResult{}, mainFailure
		},
		func(ctx context.Context) (probeBuildResult, error) {
			<-ctx.Done()
			close(probeStopped)
			return probeBuildResult{}, ctx.Err()
		},
	)
	if !errors.Is(err, mainFailure) {
		t.Fatalf("error = %v, want main failure", err)
	}
	<-probeStopped
}

func TestPreparationBuildsReportProbeFailureAfterCancelingMain(t *testing.T) {
	probeFailure := errors.New("probe build failed")
	mainStopped := make(chan struct{})
	_, _, err := runPreparationBuilds(t.Context(),
		func(ctx context.Context) (mainBuildResult, error) {
			<-ctx.Done()
			close(mainStopped)
			return mainBuildResult{}, ctx.Err()
		},
		func(context.Context) (probeBuildResult, error) {
			return probeBuildResult{}, probeFailure
		},
	)
	if !errors.Is(err, probeFailure) {
		t.Fatalf("error = %v, want probe failure", err)
	}
	<-mainStopped
}

func TestPreparationBuildsDeliverCallerCancellationToBothBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	mainStarted := make(chan struct{})
	probeStarted := make(chan struct{})
	mainStopped := make(chan struct{})
	probeStopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := runPreparationBuilds(ctx,
			func(ctx context.Context) (mainBuildResult, error) {
				close(mainStarted)
				<-ctx.Done()
				close(mainStopped)
				return mainBuildResult{}, ctx.Err()
			},
			func(ctx context.Context) (probeBuildResult, error) {
				close(probeStarted)
				<-ctx.Done()
				close(probeStopped)
				return probeBuildResult{}, ctx.Err()
			},
		)
		done <- err
	}()
	<-mainStarted
	<-probeStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	<-mainStopped
	<-probeStopped
}

func executeOptionsWithPackages(packages ...string) execute.Options {
	return execute.Options{Packages: packages}
}
