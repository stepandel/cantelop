import { expect, test } from "bun:test";
import { invitationStatus, rememberInvitation, invitationStorageKey } from "../lib/invitations";

const token = "a".repeat(64);
function storage() {
  const values = new Map();
  return { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
}

test("invitation tokens survive OAuth in this tab without entering callback URLs", () => {
  const tab = storage();
  expect(rememberInvitation(`#token=${token}`, tab)).toBe(token);
  expect(rememberInvitation("", tab)).toBe(token);
  expect(rememberInvitation("", storage())).toBeNull();
  tab.removeItem(invitationStorageKey);
  expect(rememberInvitation("", tab)).toBeNull();
});

test("a new invitation replaces the old token and a malformed link clears it", () => {
  const tab = storage();
  rememberInvitation(`#token=${token}`, tab);
  expect(rememberInvitation(`#token=${"b".repeat(64)}`, tab)).toBe("b".repeat(64));
  expect(rememberInvitation("#token=invalid", tab)).toBeNull();
  expect(rememberInvitation("", tab)).toBeNull();
});

test("invitation status distinguishes failure, expiry, waiting, and interrupted sends", () => {
  const invitation = { createdAt: new Date(0), expiresAt: new Date(120_000), delivery: "sent" };
  expect(invitationStatus(invitation, 100_000)).toBe("Submitted");
  expect(invitationStatus(invitation, 120_000)).toBe("Expired");
  expect(invitationStatus({ ...invitation, delivery: "failed" }, 100_000)).toBe("Send failed");
  expect(invitationStatus({ ...invitation, delivery: "sending" }, 10_000)).toBe("Sending…");
  expect(invitationStatus({ ...invitation, delivery: "sending" }, 100_000)).toBe("Retry send");
});
