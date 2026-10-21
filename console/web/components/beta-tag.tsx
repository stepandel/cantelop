/* The [BETA ?] marker on every page, in the front door's bar and the
   console's sidebar head: the tag is a button that opens a short
   note on what beta means here. A native popover, so it needs no client
   script — a click outside or Escape closes it. A page renders one bar or
   one sidebar, never both, so one fixed id is enough. */
export function BetaTag() {
  return (
    <>
      <button
        type="button"
        className="beta-tag beta-tag-button"
        popoverTarget="beta-note"
        aria-label="Beta: what this means"
      >
        Beta
      </button>
      <div id="beta-note" className="beta-note" popover="auto" role="note" aria-labelledby="beta-note-title">
        <p id="beta-note-title" className="beta-note-title">Cantelop is in beta</p>
        <p>
          The protocol is fully functional, but capacity to handle load is limited while the team
          refines the platform and prepares it to scale.
        </p>
        <p>Service interruptions can happen without warning.</p>
      </div>
    </>
  );
}
