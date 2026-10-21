# macOS CLI signing and notarization

The normal production path is the tag-driven GitHub Actions workflow described
below. The remaining sections document the same signing and notarization
controls for troubleshooting and manual recovery.

Linux archives do not use this procedure. Preserve them byte-for-byte while
producing the final four-platform customer release.

## Automated release workflow

`.github/workflows/publish-cli.yml` is the release authority for the CLI. A
signed `v*` tag starts one serialized release operation that:

1. verifies the SSH signature against `.github/release-signers`, requires the
   tag version to match `clients/cli/VERSION`, and requires the tagged commit to
   be reachable from `origin/main`;
2. tests the CLI and builds macOS and Linux archives with the full tagged source
   commit embedded;
3. imports the Developer ID identity into an ephemeral keychain, signs both
   macOS binaries with hardened runtime and a secure timestamp, and submits the
   exact signed binaries to Apple;
4. validates Apple's accepted log and CDHashes, rebuilds and verifies the final
   archives, and records checksums and release evidence;
5. creates or repairs the private GitHub release and verifies every uploaded
   asset byte-for-byte; and
6. calls the reusable Homebrew workflow directly after publication succeeds.

The repository must have these Actions secrets:

- `MACOS_SIGN_P12`: base64-encoded Developer ID Application identity export;
- `MACOS_SIGN_PASSWORD`: password protecting that PKCS#12 export;
- `MACOS_NOTARY_KEY`: base64-encoded App Store Connect Team API `.p8` key;
- `MACOS_NOTARY_KEY_ID`: App Store Connect API key ID;
- `MACOS_NOTARY_ISSUER_ID`: App Store Connect issuer ID; and
- `TAP_GITHUB_TOKEN`: token with Contents write access to
  `stepandel/homebrew-tap`.

Keep the unencoded certificate export and API private key out of the repository.
Repository administrators can set file-backed secrets without printing them:

```sh
base64 <DeveloperID.p12 | tr -d '\n' | gh secret set MACOS_SIGN_P12
gh secret set MACOS_SIGN_PASSWORD
base64 <AuthKey.p8 | tr -d '\n' | gh secret set MACOS_NOTARY_KEY
gh secret set MACOS_NOTARY_KEY_ID
gh secret set MACOS_NOTARY_ISSUER_ID
gh secret set TAP_GITHUB_TOKEN
```

To publish, first merge the intended version change to `main`, then create and
push an annotated signed tag from that exact commit:

```sh
git switch main
git pull --ff-only
cli_version=$(tr -d '\n' <clients/cli/VERSION)
git tag -s "v${cli_version}" -m "Release Cantelop CLI ${cli_version}"
git verify-tag "v${cli_version}"
git push origin "v${cli_version}"
```

Never move or reuse a published tag. A failed operation can be retried with the
workflow's manual `tag` input: it fills missing assets but refuses conflicting
release bytes.

## Prerequisites

The release operator needs:

- the reviewed release-qualification directory and its passing
  `artifacts/SHA256SUMS`;
- a valid `Developer ID Application` identity for the Apple Developer team;
- Xcode command-line tools with `codesign` and `notarytool`;
- an Apple Account that belongs to the same Developer team and has two-factor
  authentication enabled; and
- an Apple Account app-specific password stored through `notarytool`, or an
  App Store Connect **Team** API key.

No App Store Connect App, bundle ID, Xcode project, or App Store submission is
required for a standalone command-line tool. An app-specific password is an
Apple Account credential; its name does not imply that an App must exist.
Individual App Store Connect API keys do not support `notarytool`.

List the available signing identities and select the exact certificate
fingerprint:

```sh
security find-identity -v -p codesigning
```

Never put an app-specific password, API private key, or exported signing
identity in this repository, a release archive, a shell command, or a log.

## Store the notarization credential

Create an app-specific password at
[account.apple.com](https://account.apple.com/) under **Sign-In and Security →
App-Specific Passwords**. Give it a recognizable name such as
`Cantelop notarization`.

Store and validate it using a local Keychain profile:

```sh
xcrun notarytool store-credentials cantelop-notary \
  --apple-id "OPERATOR_APPLE_ACCOUNT" \
  --team-id "APPLE_TEAM_ID"
```

Enter the generated app-specific password only at the secure prompt.
`notarytool` must report both `Credentials validated` and
`Credentials saved to Keychain`. Validate a previously stored profile
without exposing its secret:

```sh
xcrun notarytool history --keychain-profile cantelop-notary
```

For unattended automation, use a narrowly scoped App Store Connect Team API
key instead. Store its private key in the deployment secret system and pass
`--key`, `--key-id`, and `--issuer` to `notarytool`.

## Stage and sign the qualified binaries

First verify the complete approved artifact set:

```sh
cd dist/release-qualification/QUALIFICATION_ID/artifacts
shasum -a 256 -c SHA256SUMS
```

Copy the four CLI archives to an ignored
`dist/production-release/VERSION/` directory. Extract the two macOS archives
under separate architecture-specific staging directories. Do not rebuild the
CLI and do not write into the qualification directory.

Sign each extracted macOS executable with the same Developer ID identity,
stable identifier, hardened runtime, and secure timestamp:

```sh
codesign --force \
  --sign "DEVELOPER_ID_CERTIFICATE_FINGERPRINT" \
  --identifier dev.cantelop.cli \
  --options runtime \
  --timestamp \
  staging/darwin_arm64/cantelop

codesign --force \
  --sign "DEVELOPER_ID_CERTIFICATE_FINGERPRINT" \
  --identifier dev.cantelop.cli \
  --options runtime \
  --timestamp \
  staging/darwin_amd64/cantelop
```

Strictly verify both signatures before submitting:

```sh
codesign --verify --strict --verbose=4 staging/darwin_arm64/cantelop
codesign --verify --strict --verbose=4 staging/darwin_amd64/cantelop
```

### Developer ID G2 trust troubleshooting

A certificate issued by `Developer ID Certification Authority, OU=G2` needs
Apple's Developer ID G2 intermediate certificate in the signing keychain.
Download missing intermediates only from
[Apple PKI](https://www.apple.com/certificateauthority/).

If `codesign` reports `CSSMERR_TP_NOT_TRUSTED` or an unavailable authority,
validate the public certificate chain and retry verification with the signing
keychain selected explicitly:

```sh
codesign --verify --strict --verbose=4 \
  --keychain "$HOME/Library/Keychains/login.keychain-db" \
  staging/darwin_arm64/cantelop
```

Do not weaken certificate trust settings or accept an ad-hoc signature for a
production release.

## Submit to Apple

Package the signed staging contents into a ZIP without modifying either
binary:

```sh
ditto -c -k --sequesterRsrc --keepParent \
  staging cantelop_VERSION_darwin.zip
```

Submit it and wait for the terminal result:

```sh
xcrun notarytool submit cantelop_VERSION_darwin.zip \
  --keychain-profile cantelop-notary \
  --wait \
  --output-format json
```

Stop unless the response status is exactly `Accepted`. Download the
authoritative log using the returned submission ID:

```sh
xcrun notarytool log SUBMISSION_ID \
  --keychain-profile cantelop-notary \
  notarization-log.json
```

The log must contain:

- `"status": "Accepted"`;
- `"statusSummary": "Ready for distribution"`;
- `"statusCode": 0`;
- `"issues": null`; and
- one ticket with the expected architecture and CDHash for each macOS binary.

Always inspect the log even when the submit command succeeds.

## Standalone CLI ticket behavior

Apple publishes notarization tickets for standalone executable files online,
where Gatekeeper can retrieve them. A ticket cannot currently be stapled to a
standalone binary or ZIP archive. Do not wrap Cantelop in an app, package, or
disk image solely to make stapling possible.

`spctl --assess --type execute` may report that valid standalone code “does
not seem to be an app.” That is not the notarization result. Use Apple's
`notarytool` status and downloaded log as the authoritative result, together
with strict `codesign` verification.

## Build the final customer archives

After Apple accepts the exact signed binaries:

1. recreate the two macOS `.tar.gz` archives from the signed executables and
   their existing README files;
2. keep the two qualified Linux archives unchanged;
3. regenerate `SHA256SUMS` across all four final archives;
4. extract both final macOS archives and repeat strict `codesign`
   verification;
5. run the native archive's `cantelop version` and require the approved
   version and source commit; and
6. require `shasum -a 256 -c SHA256SUMS` to pass before upload.

Record the following release evidence:

- source qualification ID and Cantelop commit;
- Developer ID display name, fingerprint, and Team ID;
- notarization submission ID and uploaded ZIP SHA-256;
- Apple status, summary, issues, and both CDHashes;
- final four archive checksums; and
- the downloaded Apple notarization log.

Signing changes the macOS archive checksums by design. It must not change the
embedded CLI version, source commit, Linux archives, or qualified program
behavior.

## Publish to Homebrew

The CLI publishing workflow calls `.github/workflows/publish-homebrew.yml`
directly after verifying the versioned GitHub release. The Homebrew workflow
also supports release events and manual retries. It treats the signed release
as immutable input: it validates all four checksums, the release evidence, and
Apple's accepted notarization log before copying the CLI archives to the public
`stepandel/homebrew-tap` release. It verifies that mirror byte-for-byte before
updating `Formula/cantelop.rb` with explicit versioned URLs and checksums.

The same workflow renders `install.sh` from that validated version and checksum
set. It publishes the script as a versioned release asset, verifies the public
copy byte-for-byte, and commits it at the tap repository root beside the formula
update. The console's `/install.sh` vanity endpoint redirects to that root file.
The installer downloads directly from the public tap release; the console never
proxies CLI archives. Release-pinned automation can execute the versioned
`install.sh` asset, while interactive installation can use:

```sh
curl -fsSL https://cantelop.com/install.sh | sh
```

The generated installer adds the default `~/.local/bin` directory to the
detected shell profile only when it is absent from PATH. The update is
idempotent and can be disabled with `CANTELOP_NO_MODIFY_PATH=1`. Custom install
directories remain caller-managed, and an existing `cantelop` elsewhere on PATH
causes installation to stop rather than shadowing Homebrew or another copy.

The workflow requires the repository Actions secret `TAP_GITHUB_TOKEN`, with
Contents write access to `stepandel/homebrew-tap`. The default `GITHUB_TOKEN`
remains read-only and scoped to the private Cantelop repository. A failed run is
safe to retry with `workflow_dispatch` and the existing Cantelop release tag;
the workflow accepts missing public archives or installer but refuses to replace
conflicting ones.
