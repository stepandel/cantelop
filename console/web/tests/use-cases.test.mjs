import { expect, test } from "bun:test";

import { SDK_REPO, USE_CASES, starterPrompt } from "../lib/use-cases.ts";

// The same pattern the create-app form enforces: the scaffold's -app slug
// has to be one `cantelop deploy` can create without a rename.
const APP_SLUG = /^(?!.*--)[a-z0-9](?:[a-z0-9-]{0,49}[a-z0-9])?$/;

test("every use case has a distinct tab and a deployable app slug", () => {
  const slugs = new Set(USE_CASES.map(({ slug }) => slug));
  const apps = new Set(USE_CASES.map(({ app }) => app));
  expect(slugs.size).toBe(USE_CASES.length);
  expect(apps.size).toBe(USE_CASES.length);
  for (const useCase of USE_CASES) {
    expect(useCase.app).toMatch(APP_SLUG);
    expect(useCase.questions.length).toBeGreaterThan(0);
  }
});

test("the prompt points the coding agent at the SDK, the serving docs, and the app slug", () => {
  const useCase = USE_CASES[0];
  const text = starterPrompt(useCase, "https://console.example.test");

  expect(text).toContain(`Build me ${useCase.agent} on Cantelop ${useCase.job}.`);
  expect(text).toContain(SDK_REPO);
  expect(text).toContain("https://console.example.test/docs");
  expect(text).toContain(`cantelop init -app ${useCase.app} -provider <my answer>`);
  expect(text).toContain("npm install @cantelop/sdk`");
  expect(text).not.toContain("@cantelop/sdk@");
  expect(text).toContain("src/cantelop.ts");
  expect(text).toContain("cantelop deploy --create-app");
  expect(text).not.toContain("src/api.ts");
  for (const question of useCase.questions) {
    expect(text).toContain(`- ${question}`);
  }
});
