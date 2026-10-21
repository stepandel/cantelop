package projectconfig

import (
	"strconv"
	"strings"
)

// Build protocol 5 adds route publication to the API artifact manifest; its
// build and watch entry points are those of protocol 4.
var SupportedSDKBuildProtocolVersions = []int{4, 5, 6}

// SDK releases before ApplicationMailboxMinimumSDKVersion lack the application-owned mailbox
// runtime capability. They still build, but their Sessions fail once the
// platform enables the application-owned mailbox, so doctor, build and deploy warn.
const ApplicationMailboxMinimumSDKVersion = "0.16.0"

const ApplicationMailboxSDKWarning = "uses the retired mailbox protocol; rebuild its Sessions for application-owned messaging. Run npm install @cantelop/sdk@latest"

// SDKLoadProgram opens every program the CLI runs against the project-installed
// SDK. Its errors say which side to upgrade and never name an SDK version: an
// SDK behind this CLI is fixed from npm, an SDK ahead of it only by a newer CLI.
// supportedProtocols is generated from SupportedSDKBuildProtocolVersions.
var SDKLoadProgram = `
async function loadSDK(...required) {
  const supportedProtocols = ` + javaScriptIntegerList(SupportedSDKBuildProtocolVersions) + `;
  // These are conditions to report, not crashes: no stack trace.
  const fail = (message) => { process.stderr.write(message + "\n"); process.exit(1); };
  let sdk;
  try {
    sdk = await import("@cantelop/sdk/build");
  } catch (error) {
    if (error?.code === "ERR_MODULE_NOT_FOUND") {
      fail("@cantelop/sdk is not installed in this project; run npm install @cantelop/sdk@latest");
    }
    if (error?.code !== "ERR_PACKAGE_PATH_NOT_EXPORTED") throw error;
    sdk = {};
  }
  const protocol = sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION;
  if (typeof protocol === "number" && protocol > Math.max(...supportedProtocols)) {
    fail("the project-installed @cantelop/sdk is newer than this CLI supports; upgrade the Cantelop CLI");
  }
  if (!supportedProtocols.includes(protocol) || required.some((name) => typeof sdk[name] !== "function")) {
    fail("the project-installed @cantelop/sdk is too old for this CLI; run npm install @cantelop/sdk@latest");
  }
  return sdk;
}
`

// javaScriptIntegerList renders values as a JavaScript array literal.
func javaScriptIntegerList(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
