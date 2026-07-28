package update

// Publish-window tolerance (#1759).
//
// A GitHub release exists before its assets do. GoReleaser creates the release,
// then uploads the platform tarballs and checksums.txt, and only then publishes
// it — but that ordering has been defeated twice (v1.10.10, v1.10.11) by a
// release that was already public at tag time, leaving `/releases/latest`
// advertising a tag with zero downloadable assets for ~11 minutes. Every
// updater path then failed with "no release binary available for <os>/<arch>",
// which reads like a broken install rather than "wait a moment".
//
// The pipeline side of that is fixed in .goreleaser.yml + release.yml (assets
// land on a draft; publishing happens only after they are verified). This file
// is the client-side belt: even against a repo mid-publish, or one released by
// some future path that regresses the invariant, the updater degrades to the
// newest release it can actually install and says so.
//
// The logic here is deliberately pure — no network, no clock, no filesystem —
// so the fallback is unit-testable.

import (
	"errors"
	"strings"
)

// ErrNoInstallableRelease reports that no release in the candidate set carries a
// usable binary for the requested platform. Callers should surface
// StillPublishingHint rather than a bare failure: the overwhelmingly likely
// cause is a release that is still uploading its assets.
var ErrNoInstallableRelease = errors.New("no release carries an installable binary for this platform")

// StillPublishingHint is the user-facing explanation for the publish window. It
// is phrased as an instruction ("try again shortly") because that is the correct
// and sufficient user action — the window closes on its own in minutes.
const StillPublishingHint = "the newest release is still publishing (its binaries are not attached yet) — try again shortly"

// HasPlatformAsset reports whether release is actually installable for
// goos/goarch: it must carry both the platform tarball and the checksums.txt
// that the tarball is verified against. Requiring both matters — a release whose
// tarballs have uploaded but whose checksums.txt has not would otherwise be
// chosen and then rejected by DownloadVerifiedBinary's integrity gate, turning a
// transient window into a hard failure.
func HasPlatformAsset(release *Release, goos, goarch string) bool {
	if release == nil {
		return false
	}
	return GetAssetURLForPlatform(release, goos, goarch) != "" && GetChecksumsURL(release) != ""
}

// SelectInstallableRelease picks the newest release in releases that can be
// installed on goos/goarch.
//
// Drafts and pre-releases are ignored, matching the semantics of GitHub's
// /releases/latest endpoint: a draft is by definition not yet offered to users,
// and falling back onto a pre-release would silently move a user off the stable
// channel.
//
// installable is the newest release with a usable binary. publishing is the
// newest eligible release that is NOT installable and is strictly newer than
// installable — i.e. the release the user would have gotten had its assets been
// attached — and is nil when the newest release is already installable. Callers
// use it to explain why they are offering an older version.
//
// When nothing is installable, installable is nil and the error is
// ErrNoInstallableRelease; publishing is still populated when a newer
// asset-less release was seen, so the caller can name it.
//
// The input need not be sorted; ordering is decided by CompareVersions.
func SelectInstallableRelease(releases []Release, goos, goarch string) (installable *Release, publishing *Release, err error) {
	var newestEligible *Release

	for i := range releases {
		candidate := &releases[i]
		if candidate.Draft || candidate.Prerelease {
			continue
		}
		if strings.TrimSpace(candidate.TagName) == "" {
			continue
		}
		if newestEligible == nil || CompareVersions(candidate.TagName, newestEligible.TagName) > 0 {
			newestEligible = candidate
		}
		if !HasPlatformAsset(candidate, goos, goarch) {
			continue
		}
		if installable == nil || CompareVersions(candidate.TagName, installable.TagName) > 0 {
			installable = candidate
		}
	}

	// Only report a publish window when the release we are skipping is newer
	// than what we settled on. An older asset-less release (e.g. a long-deleted
	// upload) is not something the user is waiting for.
	if newestEligible != nil && !HasPlatformAsset(newestEligible, goos, goarch) {
		if installable == nil || CompareVersions(newestEligible.TagName, installable.TagName) > 0 {
			publishing = newestEligible
		}
	}

	if installable == nil {
		return nil, publishing, ErrNoInstallableRelease
	}
	return installable, publishing, nil
}
