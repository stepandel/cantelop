# Console design guidelines

The console is a dense instrument panel: a neutral grey ground with white
panels raised on it, ink text, Geist for what is read and Geist Mono for what
is typed. Components separate by surface — a card with a visible border on the
ground, the way a Vercel dashboard is built — not by tint. These rules exist to
keep that density readable. Each one traces back to a concrete failure we
shipped once — don't relax them without rereading the *why*.

All tokens live in [`app/globals.css`](app/globals.css) `:root`. If a value you
need isn't a token, the answer is usually to add a token, not a literal.

## 1. The console fills the screen; proportions, not caps, keep it sane

**Rule.** The shell spans the viewport edge to edge — the console is a
full-screen tool, never a floating frame with leftover ground beside it. The
sidebar keeps its share of the width, `--sidebar-w: clamp(240px, 16vw, 340px)`,
so it scales with the monitor instead of shrinking into a strip; the pane
fills whatever remains. Reading surfaces (docs, auth) still cap their own
measure locally — full-bleed is for the operating screens, not prose.

**Why.** We shipped a centered 80rem frame and it read as wasted screen: on a
2000px monitor a third of the display was empty ground, which on an ops
surface looks like a bug, not restraint. The full-bleed trade is real —
flexible table columns absorb the extra width, so an id-to-state scan gets
longer on big monitors — and the fixed `c-*` column widths, right-aligned
numerics, and row hover are what keep long rows scannable. Do not "fix" wide
rows by reintroducing a page cap; tighten the table's own columns instead.

**Corollary.** Pinned-right items (`.topbar-right`, `.tabrow-action`) now pin
to the viewport edge; that is intended. Never size an *interior* element off
the viewport (`100vw`) — regions size off their container, which is now the
full work area.

## 2. Spacing comes from the scale; section breaks are ~3x group gaps

**Rule.** Margins, paddings, and gaps use the `--sp-*` tokens only:
`4 / 8 / 12 / 16 / 24 / 32 / 48`. `2px` is permitted as a sub-scale micro step
(dot-to-label, stat label-to-value). Section boundaries — a `.section-label`
opening a new group like *create workspace* — use `--gap-section` (32px),
roughly 3x the intra-group gap. Component *heights* (rows, inputs, bars) are
not rhythm values and may stay off-scale.

**Why.** We shipped gaps of 25/32/24/15px in one column — no common base, so
nothing aligned optically. Worse, a section break (25px) was visually
indistinguishable from an intra-section gap (24px), so the whole lower half of
the page read as one undifferentiated stack. A scale keeps gaps consistent; the
3x ratio is what makes grouping legible at all.

## 3. Table columns are sized to content, not to spare width

**Rule.** Every `.data-table` declares a `<colgroup>` using the shared column
classes in globals.css (`c-id`, `c-num`, `c-time`, `c-stamp`, `c-state`,
`c-build`, …).
Identifier and hostname columns take the flexible space; numeric, state, and
timestamp columns are fixed and narrow so the value sits immediately after the
content. Tables are `table-layout: fixed` — an undeclared colgroup means the
browser distributes free space by content length, which is exactly the bug.
New column type → new `c-*` class, not an inline width.

**Why.** Under auto layout the two lowest-information columns (a single-digit
count, a dash) ended up with the widest, most prominent tracts on the page,
while the hostname got an enormous track it didn't need. Numeric columns want
to be fixed and narrow, right after the content — not flexed toward the panel
edge.

## 4. Styling lives in the stylesheet

**Rule.** No static `style={{ … }}` in components — if it's a constant, it's a
class in `globals.css` (or the component's module). Inline styles are for
genuinely dynamic values only: computed positions, animation delays,
percentage widths derived from data.

**Why.** The scale and the column classes can only be audited (and grepped) if
values live in one place. Every inline literal is a value the next reader
can't find and the next sweep won't fix.

## 5. Type comes from the role scale; Geist for reading, Geist Mono for what is typed

**Rule.** Every font-size is a `--fs-*` token: `mark 8 / label 11 / support 12 /
data 14 / title 16 / stat 20 / display 24` (rem-defined, px at the 16px root).
Weights are 400 and 500 only. Two families, loaded in `app/layout.tsx`: Geist
(`--sans`) is the face of everything a person reads — labels, navigation,
headings, table text, notices, prose — and is what `body` sets. Geist Mono
(`--mono`) is reserved for what is literally typed or pasted: `code`, the
snippets in the docs, identifiers and digests (`.copy-id`, `.row-toggle`,
`.monitor-head > strong`), log lines (`.trace-log p`), and the keyboard hint.
A new rule that reaches for `--mono` should be able to say what the reader
will paste. Prose roles (`.empty-note`, `.table-note`, `.form-hint`,
`.head-id > span`, notices) carry `--lh-prose` (1.55) and a ~70ch measure;
dense single-line UI keeps the default leading. `slashed-zero` is set on
`body` and applies wherever the face has the glyph, so any rule that declares
`font-variant-numeric` locally must re-state it.

**Why.** We shipped the whole site in 13px IBM Plex Mono — the instrument-panel
look — and it read as an instrument panel everywhere: a sidebar label, a
notice, a docs paragraph, and a Sandbox ID all had the same texture, so
nothing was easier to find than anything else, and mono at that size was the
first thing people said was hard to read. A text face makes a label a label,
and lets mono mean something again: when a string is mono, it is something
you can paste. The ramp moved one step up for data, title, and stat because a
sans at a given size sets narrower than a mono at the same size. Before that,
we had shipped 12 arbitrary px sizes with three pairs half a pixel apart —
sub-perceptual at these sizes — and px sizes inside a rem layout, so a raised
browser default widened columns without enlarging the text inside them; the
token ramp is what keeps both from coming back.

**Corollary.** Text on `--panel`, `--hover`, or `--active` (sidebar counts,
highlighted rows, hot palette rows) can't use `--faint`/`--grey` — those tiers
hold AA only on `--bg` and `--surface`. Use `--muted`, or add a state rule next
to the component (see the `tr:is(:hover, .selected)` block).

## 5b. The home page is the one surface allowed above the ramp

**Rule.** `/` (`app/page.tsx` + `app/home.module.css`) is public marketing and
may use two roles the console never gets: `--fs-lede` (16px) for the subhead
and `--fs-hero` (the only fluid size in the system, clamping 28→44px) for the
headline. Everything else on that page is ordinary console vocabulary — same
ground, same faces, `--sp-*` spacing, and no static inline styles. Band boundaries are a hairline plus `--sp-12` on each
side, which is how a 96px section break stays on the scale without a new token.
Nothing in the console may reach for these two tokens.

**Why.** The console is read leaned in by someone who already signed in; the
home page is read at a glance by someone who has never seen the product, and a
24px `--fs-display` headline reads as a caption at that distance. The headline
is a fixed string, so clamping it holds its line count from 375px to ultrawide
instead of rewrapping mid-phrase. `--fs-lede` exists because the jump from a
44px display straight to 14px body has no subhead in it. The exception is
scoped to the front door precisely so the instrument scale stays intact.

**Corollary.** The root layout sets `robots: { index: false }` for the whole
console; the home page overrides it to `index: true`. It is the only page that
should.

## 5c. Reading surfaces get a ramp of their own

**Rule.** The docs and the blog — anything rendered through `<Prose>` — set
their running text on the reading ramp: `read 16 / read-small 14 / read-h3 18 /
read-h2 22 / read-h1 32`, code at `--fs-code` (14) whether inline or in a
block, leading `--lh-read` (1.6), a measure of about 600px (632px for a blog
post), two thirds of a line (`--sp-4`) between paragraphs and twice that
(`--sp-8`) above an h2. A post's title is the ramp's h1, never `--fs-hero`.
Links in prose are ink with a grey underline. Mono keeps what is literally
typed on those surfaces: code and commands. The console never reaches for a
`--fs-read-*` token.

**Why.** A docs page is read for minutes at a stretch, not scanned, and the
instrument ramp's 14px body on a full-width measure is the wrong instrument for
that. The long-form pages at vercel.com, cursor.com, and mitchellh.com share
one shape — a 16–18px text face on 1.5–1.75 leading, near-black ink, a
paragraph gap of well under a line, headings a step or two above the body
with most of their air above, links underlined in the text color, mono only
for code — and that shape is what the ramp encodes. The first cut of the ramp
sat at the top of that band (17px, a 24px h2 under 48px of air, a 44px post
title) and a desktop screen held about a fifth less of a post than those
pages do; the ramp now sits at the bottom of it. The
exception is scoped to `<Prose>` so the instrument scale stays intact
everywhere else.

**Corollary.** Section headings rendered from markdown carry an id derived
from their text and an anchor that shows on hover, so `/docs/cli#operations`
is a link that lands. Write `## Runtime configuration`, never a raw `<h2 id>`.

**Corollary.** Code blocks are highlighted in four roles — keyword, string,
comment, constant — drawn from the state palette (`--blue`, `--amber`,
`--muted`, `--green`), not an editor theme. The grammar sorts on the server
(`components/markdown/highlight.ts`); the stylesheet colors. A new role is a
new class in `prose.module.css`, never an inline color.

**Corollary.** A code block is a panel with a strip along its top edge, the
same shape as the home page's terminal: the strip names the block — the file
the fence was titled with (```` ```ts title="src/cantelop.ts" ````), the tab strip
when a page offers variants, or the kind of thing it is (Terminal, TypeScript)
— and holds the copy control, so nothing ever sits over the code. A file's
lines are numbered in a gutter drawn by CSS counter, never in the DOM. A fence
that shows a file names it; a transcript or a listing does not.

## 5c′. Figures draw the claim their caption makes

**Rule.** A figure on a docs or blog page is an inline SVG built from the
primitives in `components/figure/figure.tsx`, laid out by hand in its own
units and scaled to the measure. One grammar, so the same shape means the
same thing on every page: the **subject** (the one thing the paragraph is
about) gets a 2px ink stroke; a **component** is the site's card; a
**context** is a panel fill with a corner label, never a dashed box; dashed
means **absent** (gone or not yet here); a **solid** ink fill is shared
substrate. Connectors are lines with real heads and their label on the
line; a **blocked** path is red and stops at a wall with a cross; when the
point is lifetime, the picture gets a time **axis** and durable things are
bars that span it. Type inside a drawing is 15px titles and 12px detail at
scale, never 11px. Every figure carries `role="img"` and an `aria-label`
that states the claim, and a `<figcaption>` that says what the picture
cannot.

**Why.** The first figures were CSS grids of identical 1px boxes joined by
arrow glyphs. Consistent, but with no hierarchy the picture had no subject,
the arrows were the faintest marks on the page, and the mechanism ("the
firewall blocks…", "the hardware is still shared") lived in the caption
rather than the drawing. A figure earns its place by showing what the
prose would otherwise have to assemble in the reader's head; if the
caption is doing that work, the drawing is not.

## 5d. Sentence case, everywhere a person reads

**Rule.** Every heading, label, nav item, button, notice, placeholder, and
markdown heading starts with a capital letter and is otherwise sentence case:
"Quick start", "New app", "Build history is temporarily unavailable". Product
names and acronyms keep their own case (CLI, API, SDK, SSE, GitHub, OpenAI,
curl). Uppercase tracked labels (`.section-label`, `th`, `.stat span`) are
produced by CSS from sentence-case source, never written in caps. State words
that arrive from the API in lowercase (`running`, `terminated`) are shown in
sentence case by the state cell's `text-transform`, not rewritten in data.
Slugs, IDs, hostnames, and anything the reader will type stay exactly as they
are.

**Why.** We shipped everything lowercase as a style. It read as unfinished,
and it hid the two things capitals carry: where a sentence starts, and which
words are names. "session runtime" and "Session runtime" are different claims
about what a Session is.

## 6. Components separate by surface, not by tint

**Rule.** The work area is the grey ground (`--bg`); everything that holds
content sits on it as a white panel (`--surface`) with a `--line` border,
`--radius-lg` corners, and the card gutter (`--sp-4`) around it. The
`.section-panel` is that panel: tables, notes, forms, and an opened row's
monitor stack inside it edge to edge, and the card's outline is what
separates one component from the next. Graph panels, app cards, and the
operator's infrastructure blocks are the same card. Chrome is white too — the
sidebar and topbar are `--surface` ruled off the ground by `--line`, and the
current nav item is a filled `--active` pill. A notice on the ground is a
boxed banner in its state's wash and outline; inside a panel it flattens to
a band. Row dividers inside a panel stay `--hairline`; a panel never draws a
rule where its own edge already is.

**Why.** We shipped the whole console on one warm off-white with hairline
rules doing all the work: sidebar, pane, tables, and cards were within a few
percent of each other's luminance, so nothing read as a distinct component
and the borders were too faint to carry the structure alone. Vercel's
dashboard gets its legibility from surface: a white card on a `#fafafa`
ground separates by value before its border is even seen, and the border is
then free to be a real line. That is the construction here. Don't reach for
a tint (`--panel`, `--hover`) to mark a region — those are fills for a state
inside a surface, not grounds.

**Corollary.** Color only where state demands it (the `dot-*` / `st-*` /
`http-*` families); uppercase `--fs-label` labels for metadata;
`tabular-nums` for anything that counts. Shadows are the two card tokens
(`--shadow-card` at rest, `--shadow-card-hover` under the pointer) and
`--shadow-pop` for a dialog — never a literal.

The home page's app grid (`.app-grid` / `.app-card`) is a destination rather
than a data surface, so it answers the pointer: the border darkens and the
card lifts.
The grid's trailing tile, "+ new app", opens the create form on `#new-app`.
An account with no apps yet gets a second tile beside it, "start from an
example", which on `#example` raises the front door's use-case prompts as a
native `<dialog>` (`.example-dialog`) cut from the palette's cloth — chips
from `.tabrow`, a `.monitor-head`, the prompt in a block drawn like an input.
It is the one tutorial the console shows, and it stays off the screen until
asked for.
A screen header holds at most four `.stat` blocks at rest — secondary facts go
behind one `.head-more` disclosure (which reuses the `monitor-meta` pair
grammar). Tables show what the screen's disclosure can't: a column whose data
already lives in an opened row's monitor or detail view is a column to drop,
not to squeeze.
When adding a screen, start from an existing one (`app-screen` plus a section
page, or `dashboard-view`) — the shell, screen-head, sidebar section nav,
table, section-label, inline-form stack is the whole vocabulary, and a new
screen should not need new CSS beyond a class or two. An App's screens are
pages listed in the sidebar, not tabs: `/apps/[id]` itself is the App's home
(the App-wide header over its runtime graphs, reached through the App's name
in the sidebar), and the rest live at `/apps/[id]/<section>` (workspaces,
builds, configuration, api, then the monitoring group). A page below a
section, such as one route under the API section at `/apps/[id]/api/route`,
names itself as the breadcrumb leaf through `useSectionCrumb` and keeps the
section's sidebar item active. The shell's sidebar lists
an App's sections while one is open and the account's nav otherwise — apps
themselves live as cards on `/profile`, the console home. The operator
console mirrors the same shape: `/admin` is the host's home (its header over
the infrastructure graphs, reached through the sidebar's telemetry row), with
sessions, sandboxes, requests and messages as pages at `/admin/<section>`.

## 7. Timestamps are drawn in the viewer's zone, through `useClock()`

**Rule.** A component that shows an instant takes its formatters from
`useClock()` (`lib/use-clock`), never from `lib/format` directly — that module
exports `clockFor(zone)`, not a bound formatter. A reading that a column header
does not date carries its own date (`formatStamp`, the `c-stamp` column) once
the table can hold more than a day of records; the full reading with its named
zone goes in the cell's `title`.

**Why.** The viewer's zone is a browser fact the server pass cannot resolve, so
a formatter bound at module load either shows everyone UTC or mismatches on
hydration. The hook hands the server UTC and re-renders with the browser's zone
the moment it is known, which is also why importing a formatter directly leaves
a component stuck on UTC: nothing re-renders it.

## Checklist for a new or changed screen

- [ ] Renders inside `ConsoleShell` (and therefore `.pane`) — nothing sized off the viewport
- [ ] Every spacing value is an `--sp-*` token (or 2px micro)
- [ ] Every font-size is an `--fs-*` token; weights 400/500 only; prose gets `--lh-prose` and a measure
- [ ] `--mono` only on something the reader could paste; `--fs-read-*` only inside `<Prose>`
- [ ] A figure is drawn with `components/figure` primitives, has one subject, labels its connectors, and draws what its caption claims
- [ ] Every visible string starts with a capital; acronyms and product names keep their case
- [ ] Nothing sets `--faint`/`--grey` text on a `--panel`/`--hover`/`--active` fill without a state rule stepping it up
- [ ] Content lives in a `.section-panel` (or an equivalent card) on the ground; nothing draws its own outline beside one
- [ ] New sections open with `.section-label` (which carries `--gap-section`)
- [ ] Tables declare a `<colgroup>`; numeric/time/state columns use fixed `c-*` classes
- [ ] Timestamps come from `useClock()`; a table spanning days uses `formatStamp` + `c-stamp`
- [ ] No static inline styles
- [ ] Checked at 1280, 1680, and 375px wide — `/preview/*` pages render every screen with synthetic data and no backend (`/preview/app?section=<id>` for each App section)

## Brand on the public site

**Rule.** The front door — home, docs, blog — wears the brand: warm ground
`#EBE8E1`, deep phthalo ink `#0B2E22`, cantaloupe `#DF7A2E` as the single
accent, the mark from `components/brand-mark.tsx` beside a lowercase Sora
wordmark. It is applied as one scope class, `.site`, on each of those
surfaces' roots; the class re-points the console tokens (`--bg`, `--ink`,
`--line`, the text tiers, `--blue` for links) to the brand values, so every
component built on the tokens takes the brand for free. The console proper
does not carry the class and keeps the neutral instrument palette.

**Why.** The brand was decided as a palette, not a component set; scoping it
through the tokens means the docs' prose, the blog's cards, and the topbar all
moved together without a second stylesheet, and the console — read leaned in
for hours — kept the greys that were tuned for it.

**Corollaries.** Cantaloupe is a shape colour: 2.5:1 on the warm ground, so it
fills the mark's facet and the hero's Get started button and is never small
text. The tag stays in amber-family `--cantaloupe-deep` where it is typed
large (social images). The mark's knockout stroke must match the surface it
sits on — pass `knockout` when it leaves `--surface`. Below 32px use the
component's `small` cut, which is also what `app/icon.svg` draws.
