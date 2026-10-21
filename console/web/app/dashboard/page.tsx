import { redirect } from "next/navigation";

// The apps overview merged into the account home: /profile shows every app as
// a card and owns creation. Old links (including /dashboard#new-app — the
// fragment survives the redirect) land there.
export default function Dashboard() {
  redirect("/profile");
}
