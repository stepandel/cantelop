package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

// cliHandler runs a command with the arguments that follow its name.
type cliHandler func(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error

// cliCommand is one row of the command table. The table is the only list of
// commands: it drives dispatch, unknown-command detection, the usage error and
// the help text.
type cliCommand struct {
	name string
	// run handles the command's arguments. A command without run accepts only
	// its subcommands; one with run receives every other second word.
	run         cliHandler
	subcommands []cliCommand
	help        []cliHelpEntry
}

// cliHelpEntry is one line of the help text, listed under section.
type cliHelpEntry struct {
	section string
	usage   string
	summary []string
}

const (
	helpProject    = "Project"
	helpOperations = "Operations"
	helpResources  = "Resources"
	helpAccount    = "Account"
)

var helpSections = []string{helpProject, helpOperations, helpResources, helpAccount}

// errCommandUsage asks dispatch to report the generated usage error.
var errCommandUsage = errors.New("usage")

func helpLine(section, usage string, summary ...string) []cliHelpEntry {
	return []cliHelpEntry{{section: section, usage: usage, summary: summary}}
}

var cliCommands = []cliCommand{
	{name: "init", run: func(_ context.Context, args []string, _ io.Reader, output io.Writer, _ *http.Client) error {
		return runProjectInit(args, output)
	}, help: helpLine(helpProject, "init", "Create a project manifest or agent starter")},
	{name: "doctor", run: func(ctx context.Context, args []string, _ io.Reader, output io.Writer, client *http.Client) error {
		return runDoctor(ctx, args, output, client)
	}, help: helpLine(helpProject, "doctor", "Check project files, tools, authentication, and configuration")},
	{name: "dev", run: func(ctx context.Context, args []string, _ io.Reader, output io.Writer, _ *http.Client) error {
		return runProjectDev(ctx, args, output)
	}, help: helpLine(helpProject, "dev", "Run the API and Session runtime locally")},
	{name: "build", run: func(ctx context.Context, args []string, _ io.Reader, output io.Writer, _ *http.Client) error {
		return runProjectBuild(ctx, args, output)
	}, help: helpLine(helpProject, "build", "Build and qualify a release locally")},
	{name: "deploy", run: runProjectDeployCommand, help: helpLine(helpProject, "deploy", "Build and deploy the current project"),
		subcommands: []cliCommand{
			{name: "cancel", run: withoutInput(runDeploymentCancel),
				help: helpLine(helpProject, "deploy cancel", "Cancel an upload or a release (--release RELEASE_ID)")},
			{name: "restart", run: withoutInput(runDeploymentRestart),
				help: helpLine(helpProject, "deploy restart", "Restart a release using its uploaded artifacts")},
		}},
	{name: "env", subcommands: []cliCommand{
		{name: "sync", run: withoutInput(runProjectEnvSyncCommand),
			help: helpLine(helpProject, "env sync", "Sync declared environment variables and secrets from a file")},
	}},
	{name: "routes", run: withoutInput(runRoutes), help: []cliHelpEntry{
		{section: helpOperations, usage: "routes", summary: []string{"List the current App's API routes with their traffic", "(--openapi writes them as an OpenAPI document)"}},
		{section: helpOperations, usage: "routes METHOD PATH", summary: []string{"Show one route: HTTP outcomes and the Session work it started"}},
	}},
	{name: "logs", run: withoutInput(runLogs), help: helpLine(helpOperations, "logs", "Show request logs for the current App")},
	{name: "traces", run: monitoringCommand("traces"), help: helpLine(helpOperations, "traces [ID]", "Show recent traces for the current App")},
	{name: "sandboxes", run: monitoringCommand("sandboxes"), help: helpLine(helpOperations, "sandboxes [ID]", "Show the current App's sandbox snapshot")},
	{name: "session", subcommands: []cliCommand{
		{name: "list", run: withoutInput(runSessionList), help: helpLine(helpOperations, "session list", "List Sessions for the current App")},
		{name: "inspect", run: sessionActionCommand("inspect"), help: helpLine(helpOperations, "session inspect SESSION_ID", "Show details for a Session")},
		{name: "terminate", run: sessionActionCommand("terminate"), help: helpLine(helpOperations, "session terminate SESSION_ID", "Terminate a Session")},
	}},
	{name: "releases", run: withoutInput(runReleases), help: helpLine(helpOperations, "releases", "List releases for the current App")},
	{name: "rollback", run: withoutInput(runRollback), help: helpLine(helpOperations, "rollback", "Roll back the current App to its previous release")},
	{name: "database", subcommands: []cliCommand{
		{name: "migrations", run: withoutInput(runDatabaseMigrations),
			help: helpLine(helpOperations, "database migrations WORKSPACE_ID", "Inspect system and application migration history")},
	}},
	{name: "app", subcommands: []cliCommand{
		{name: "list", run: appCommand(runAppList), help: helpLine(helpResources, "app list", "List Apps")},
		{name: "inspect", run: appCommand(runAppInspect), help: helpLine(helpResources, "app inspect APP_ID", "Show an App")},
		{name: "create", run: appCommand(runAppCreate), help: helpLine(helpResources, "app create", "Create an App")},
		{name: "archive", run: appCommand(runAppArchive), help: helpLine(helpResources, "app archive APP_ID", "Archive an App")},
		{name: "deploy", run: appCommand(runAppDeploy), help: helpLine(helpResources, "app deploy", "Deploy prebuilt API and Session runtime artifacts")},
		{name: "env", run: configCommand("env"), help: helpLine(helpResources, "app env", "Set, list, or unset App environment variables")},
		{name: "secret", run: configCommand("secret"), help: helpLine(helpResources, "app secret", "Set, list, or unset App secrets")},
		{name: "credentials", run: appCommand(runAppCredentials), help: []cliHelpEntry{
			{section: helpResources, usage: "app credentials", summary: []string{"Print the credentials a backend uses to call the App", "(--format dotenv|app-config|token)"}},
			{section: helpResources, usage: "app credentials rotate", summary: []string{"Issue a new token; the previous one works for --grace (24h)", "or stops at once with --now"}},
		}},
	}},
	{name: "login", run: withoutInput(runLogin), help: helpLine(helpAccount, "login", "Authenticate with the Cantelop console")},
	{name: "version", run: func(_ context.Context, args []string, _ io.Reader, output io.Writer, _ *http.Client) error {
		if len(args) != 0 {
			return errCommandUsage
		}
		_, err := fmt.Fprintf(output, "cantelop %s (%s)\n", version, commit)
		return err
	}, help: helpLine(helpAccount, "version", "Print the CLI version and source commit")},
}

func withoutInput(run func(context.Context, []string, io.Writer, *http.Client) error) cliHandler {
	return func(ctx context.Context, args []string, _ io.Reader, output io.Writer, client *http.Client) error {
		return run(ctx, args, output, client)
	}
}

func monitoringCommand(kind string) cliHandler {
	return func(ctx context.Context, args []string, _ io.Reader, output io.Writer, client *http.Client) error {
		return runMonitoring(ctx, kind, args, output, client)
	}
}

func sessionActionCommand(action string) cliHandler {
	return func(ctx context.Context, args []string, _ io.Reader, output io.Writer, client *http.Client) error {
		return runSessionAction(ctx, action, args, output, client)
	}
}

// appCommand runs an App resource command with the stored CLI login.
func appCommand(run func(context.Context, []string, io.Writer, *controlapi.Session) error) cliHandler {
	return func(ctx context.Context, args []string, _ io.Reader, output io.Writer, client *http.Client) error {
		session, err := newTenantSession(client)
		if err != nil {
			return err
		}
		return run(ctx, args, output, session)
	}
}

func configCommand(kind string) cliHandler {
	return func(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error {
		session, err := newTenantSession(client)
		if err != nil {
			return err
		}
		return runConfigCommand(ctx, append([]string{kind}, args...), input, output, session)
	}
}

func findCLICommand(commands []cliCommand, name string) *cliCommand {
	for index := range commands {
		if commands[index].name == name {
			return &commands[index]
		}
	}
	return nil
}

// unknownCommand reports a first word, or a required subcommand, that is not
// in the command table.
func unknownCommand(args []string) error {
	if len(args) == 0 {
		return nil
	}
	command := findCLICommand(cliCommands, args[0])
	if command == nil {
		return fmt.Errorf("unknown command %q", args[0])
	}
	if len(args) > 1 && command.run == nil && findCLICommand(command.subcommands, args[1]) == nil {
		return fmt.Errorf("unknown %s command %q", args[0], args[1])
	}
	return nil
}

func dispatchCommand(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error {
	err := errCommandUsage
	if len(args) > 0 {
		if command := findCLICommand(cliCommands, args[0]); command != nil {
			err = dispatchCLICommand(ctx, command, args[1:], input, output, client)
		}
	}
	if errors.Is(err, errCommandUsage) {
		return errors.New(commandUsage())
	}
	return err
}

func dispatchCLICommand(ctx context.Context, command *cliCommand, args []string, input io.Reader, output io.Writer, client *http.Client) error {
	if len(args) > 0 {
		if subcommand := findCLICommand(command.subcommands, args[0]); subcommand != nil {
			return dispatchCLICommand(ctx, subcommand, args[1:], input, output, client)
		}
	}
	if command.run == nil {
		if len(args) > 0 {
			return fmt.Errorf("unknown %s command %q", command.name, args[0])
		}
		return errCommandUsage
	}
	return command.run(ctx, args, input, output, client)
}

// commandUsage lists every command form: a command that only accepts
// subcommands is listed once per subcommand.
func commandUsage() string {
	var forms []string
	for _, command := range cliCommands {
		if command.run == nil {
			for _, subcommand := range command.subcommands {
				forms = append(forms, command.name+" "+subcommand.name)
			}
		} else {
			forms = append(forms, command.name)
		}
	}
	return "usage: cantelop " + strings.Join(forms, "|")
}

var helpText = renderHelp()

func renderHelp() string {
	var help strings.Builder
	help.WriteString(`Cantelop CLI

Build, deploy, and operate Cantelop Apps.

Usage:
  cantelop <command> [options]
`)
	for _, section := range helpSections {
		help.WriteString("\n" + section + ":\n")
		var write func([]cliCommand)
		write = func(commands []cliCommand) {
			for _, command := range commands {
				for _, entry := range command.help {
					if entry.section == section {
						writeHelpEntry(&help, entry)
					}
				}
				write(command.subcommands)
			}
		}
		write(cliCommands)
	}
	help.WriteString(`
Options:
  -h, --help    Show this help
  -v, --version Print the CLI version and source commit

Common examples:
  cantelop init -app hello -provider openai
  cantelop dev
  cantelop deploy --dry-run
  cantelop deploy
  cantelop logs --follow

See https://cantelop.com/docs for full command documentation.
`)
	return help.String()
}

// writeHelpEntry aligns summaries in one column; a usage too wide for the
// column puts its summary on the next line.
func writeHelpEntry(help *strings.Builder, entry cliHelpEntry) {
	const indent = "                "
	wrapped := len(entry.usage) > len(indent)-2
	if wrapped {
		help.WriteString("  " + entry.usage + "\n")
	} else {
		fmt.Fprintf(help, "  %-13s ", entry.usage)
	}
	for index, line := range entry.summary {
		if index > 0 || wrapped {
			help.WriteString(indent)
		}
		help.WriteString(line + "\n")
	}
}
