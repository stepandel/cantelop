// Route fallbacks: what paints the instant a link is followed, while the
// server reads the account. The pane line rides inside a section; the shell
// stands in for the whole frame until the App or Home screen arrives.

export function PaneLoading() {
  return (
    <div className="notice status loading" role="status">
      <span className="spinner" aria-hidden="true" />
      Loading…
    </div>
  );
}

export function ShellLoading() {
  return (
    <div className="shell" aria-busy="true">
      <aside className="sidebar"><div className="sidebar-head" /></aside>
      <div className="workarea">
        <header className="topbar"><div className="topbar-inner" /></header>
        <div className="content"><div className="pane"><PaneLoading /></div></div>
      </div>
    </div>
  );
}
