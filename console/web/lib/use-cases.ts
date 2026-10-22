import { SDK_REPO } from "./repositories";

/* The starter use cases, shared by the front door and the console's example
   dialog. Seven jobs, one prompt each. The prompt is written for whatever
   coding agent the reader already uses — paste it, and that agent builds the
   Cantelop project. Each prompt is one sentence of setup, the SDK and docs
   links, and the questions the coding agent should put to its user before it
   writes anything: the shape of the agent is the user's call, not ours. The
   docs link carries the serving origin, which the caller fills in after
   mount, so the prompt points at whichever deployment served it. */

/* 24-unit stroke glyphs, one per use case. Drawn here rather than pulled
   from an icon set: seven glyphs are not worth a dependency, and drawing
   them keeps the stroke weight matched to the hairline grammar of both
   surfaces that show them. */
export const ICON_PATHS = {
  hash: "M4 9h16M4 15h16M10 3L8 21M16 3l-2 18",
  mail: "M2 6a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2zM22 7l-10 6L2 7",
  globe: "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM2 12h20M12 2a15 15 0 0 1 4 10 15 15 0 0 1-4 10 15 15 0 0 1-4-10 15 15 0 0 1 4-10z",
  pullRequest: "M6 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM13 6h3a2 2 0 0 1 2 2v7M6 9v12",
  list: "M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01",
  send: "M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z",
  plus: "M12 5v14M5 12h14",
} as const;

export type UseCaseIcon = keyof typeof ICON_PATHS;

export type UseCase = {
  slug: string;
  icon: UseCaseIcon;
  /* The tab's label. The full label titles the panel. */
  short: string;
  label: string;
  /* How the prompt opens: "Build me ${agent} on Cantelop ${job}." */
  agent: string;
  job: string;
  /* The App slug the scaffold targets — what `cantelop deploy` creates. */
  app: string;
  /* What the coding agent should find out before it builds. */
  questions: readonly string[];
};

export { SDK_REPO };

export const USE_CASES: readonly UseCase[] = [
  {
    slug: "slack-ops",
    icon: "hash",
    short: "Slack",
    label: "Slack agent that monitors production",
    agent: "a Slack agent",
    job: "that monitors production",
    app: "ops-bot",
    questions: [
      "Which channel does it live in, and should it respond only when mentioned or follow threads too?",
      "Where do alerts come from — Alertmanager, PagerDuty, Grafana, something else — and what does a payload look like?",
      "What can it read about production: health URLs, a logs or metrics endpoint, deploy history?",
      "Should it only report, or may it act — restart, roll back, page someone?",
    ],
  },
  {
    slug: "email-support",
    icon: "mail",
    short: "Email",
    label: "Email customer support agent",
    agent: "an email agent",
    job: "that handles customer support",
    app: "support-agent",
    questions: [
      "Which inbound email provider — Postmark, SendGrid, Resend, something else — and which address?",
      "Where does product knowledge come from: a docs folder, a help center, past tickets?",
      "When must it hand off to a human, and how: a draft for review, a forward, a ticket?",
      "What tone and signature, and should it send directly or queue replies for approval at first?",
    ],
  },
  {
    slug: "site-widget",
    icon: "globe",
    short: "Site",
    label: "Agent embedded in your site",
    agent: "a chat agent",
    job: "that's embedded in my site",
    app: "site-chat",
    questions: [
      "Which site and pages, and how should it be embedded: a script tag, an iframe, a React component?",
      "What should it answer from: a sitemap, a list of URLs, a docs repo?",
      "Should it remember a visitor across visits, and for how long?",
      "What should it refuse or hand off — pricing, account questions, anything the docs don't cover?",
    ],
  },
  {
    slug: "github-qa",
    icon: "pullRequest",
    short: "GitHub",
    label: "QA agent that can be tagged in GitHub PR",
    agent: "a QA agent",
    job: "that I can tag in a GitHub PR",
    app: "pr-qa",
    questions: [
      "Which repositories, and how is it summoned: a mention in a PR comment, every new PR, a label?",
      "What should it run: the project's tests and lint, a build, a manual walkthrough of the change?",
      "How should it report: a review with inline comments, one summary comment, a check run?",
      "May it approve or request changes, or only comment?",
    ],
  },
  {
    slug: "linear-triage",
    icon: "list",
    short: "Linear",
    label: "Linear agent that triages tickets",
    agent: "a Linear agent",
    job: "that triages tickets",
    app: "linear-triage",
    questions: [
      "Which teams and projects, and which events: new issues only, or edits too?",
      "What may it set: priority, labels, project, assignee, estimate?",
      "What are the team's conventions for those, or should it learn them from past issues?",
      "Should it comment with its reasoning, and how do humans correct it when it's wrong?",
    ],
  },
  {
    slug: "telegram-docs",
    icon: "send",
    short: "Telegram",
    label: "Telegram agent that has access to your documents",
    agent: "a Telegram agent",
    job: "with access to my documents",
    app: "docs-bot",
    questions: [
      "Where do the documents live: a git repo, a bucket, a folder of URLs, files sent to the bot?",
      "Who may use it: one private chat, a group, anyone with the link?",
      "Should answers quote their source, and should it say when the documents don't cover a question?",
      "Should documents sent to the bot be added to the set?",
    ],
  },
  {
    slug: "your-own",
    icon: "plus",
    short: "Your own",
    label: "Your own use case",
    agent: "an agent",
    job: "for my own use case",
    app: "my-agent",
    questions: [
      "What job does it do, and what does a good outcome look like?",
      "What wakes it: a webhook from which service, a chat message, a call from my own app, a schedule?",
      "What does one conversation belong to: a user, a thread, a ticket, a repository?",
      "What does it need access to, and what should it remember between requests?",
      "What must it never do without a human?",
    ],
  },
];

export function starterPrompt(useCase: UseCase, origin: string) {
  return `Build me ${useCase.agent} on Cantelop ${useCase.job}. Cantelop is a hosted runtime where each agent session runs in its own microVM with a workspace that outlives it.

Use the Cantelop SDK: ${SDK_REPO}
Docs for reference: ${origin}/docs

Before you write any code, ask me:
${useCase.questions.map((question) => `- ${question}`).join("\n")}
- Whose Workspace is it (if there's more than one user or environment to the agent): a user, a customer account, a team, a codebase? Cantelop creates workspaces that could be shared between sessions, so pick the identity that owns the files.
- Which agent SDK should the runtime use: openai, claude, pi, or something else?

Then scaffold with \`cantelop init -app ${useCase.app} -provider <my answer>\` and install the SDK with \`npm install @cantelop/sdk\`. Cantelop SDK 1.0 declares the App in code: \`cantelop.json\` only points at \`src/cantelop.ts\`, which exports \`cantelop.app({ name: "${useCase.app}", runtime, environment, webhooks })\`, and the Session runtime lives in \`src/agent.ts\`. Don't write an HTTP API: Cantelop generates the App's Edge Worker. Declare each inbound webhook on the App under \`/webhooks/...\` with a handler that verifies the provider's signature and dispatches to \`app.workspace({ slug }).session({ id })\`, and declare every env var and secret in \`environment\`. Run it with \`cantelop dev\`, deploy with \`cantelop deploy --create-app\`, then tell me which env vars and secrets to set (\`cantelop deploy --env-file .env\` or \`cantelop env sync\` imports the declared ones) and which webhook URL to register at https://${useCase.app}.cantelop.dev.`;
}
