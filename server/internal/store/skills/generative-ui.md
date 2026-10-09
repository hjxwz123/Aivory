# Generative UI

Choose the answer's presentation proactively, using the whole conversation, supplied data, and the answer you are about to give. Users do not need to say "visualize", "chart", "interactive", or name this Skill. A vague request can still warrant a visual answer if it makes the substance easier to understand, compare, explore, or act on. When a visualization clearly helps, include it directly rather than asking whether the user wants one.

## Decide before answering

1. Identify the user's actual task from the conversation, not just keywords in the latest message. "What do you think?", "Help me make sense of this", "How should I arrange it?", or "What happens if I change this?" may refer to data, options, a plan, or a formula in earlier messages.
2. Ask whether a small visual or interactive view would materially reduce reading effort, reveal a pattern, clarify a sequence, or let the user explore alternatives. If yes, use the smallest appropriate component alongside a brief direct answer. Do not wait for an explicit visualization request.
3. Prefer the presets for comparisons and organized information. Use inline HTML when adjusting inputs, manipulating a diagram, or exploring cause and effect adds value that a static component cannot provide.
4. Use prose when a short explanation already answers the question, or when visualization would mainly decorate the answer. Respect requests for plain text, code only, a specific format, or no visualization. Never replace an essential explanation or requested deliverable with an interface.

### Match the substance to a component

- Several comparable choices or repeated attributes: a compact table, even when the user only asks "Which should I choose?". Explain the recommendation outside it. Tabs are useful for substantial alternative scenarios, not two short sentences.
- Reliable values across periods or categories: a line or bar chart with meaningful units; metrics only for genuinely useful totals or headline values. A request to "look at these numbers" is sufficient. Charts require actual data from the conversation, tools, or an explicit formula, not plausible-looking invented values.
- A plan, workflow, tutorial, or ordered troubleshooting sequence: steps. Use tabs or accordions when substantial independent sections would otherwise be difficult to scan. Do not manufacture a timeline or extra sections to fill a component.
- Changing inputs can answer "what if": a small inline HTML calculator or simulation with labeled inputs, units, stated assumptions, and immediate results. Use neutral editable example values only when they are clearly marked as examples; ask for missing critical facts when a real answer depends on them.
- An explanation of a spatial, geometric, or mechanical relationship: a compact interactive diagram if manipulation helps understanding. A definition or short conceptual answer usually needs only prose.

### Implicit-intent examples

- Earlier message: monthly token counts. Latest request: "Does this look normal?" → explain the change and include a trend chart. Do not claim statistical normality without a relevant baseline.
- Earlier message: two deployment options and costs. Latest request: "Help me decide." → compare actual attributes in a table and give a recommendation.
- Request: "I have two weeks to prepare; how do I get started?" → show a concise sequence of steps based on the stated goal, with prose for key priorities.
- Earlier message: loan principal, rate, and term. Latest request: "What if I repay earlier?" → offer an inline repayment calculator using an explicit formula and assumptions.
- Request: "Thanks", a one-line factual question, translation, or "just give me the code" → follow the requested ordinary response format.

Keep the first useful conclusion visible before a larger visualization. Usually use one small visualization per answer, not a dashboard of every supported component. Match the user's language. Explain uncertainty and assumptions; do not silently turn qualitative judgments into numerical scores. Do not announce that you are invoking a Skill or ask the user to learn the output protocol.

## Preset UI (preferred)

Output a fenced `aivory-ui` block containing valid JSON, without comments. Root: `{"version":1,"title":"optional heading","description":"optional explanation","blocks":[...]}`.

Supported blocks and exact properties:

- `{"type":"text","text":"Plain text, not Markdown or HTML"}`
- `{"type":"metrics","items":[{"label":"Revenue","value":"$1,240","hint":"optional context"}]}`
- `{"type":"chart","kind":"line","title":"optional","labels":["Jan","Feb"],"series":[{"name":"Revenue","values":[120,180]}]}`. Kind is `line` or `bar`. Every series has exactly as many finite numeric values as labels. Max 6 series and 120 points. Include real units in series names or surrounding explanation.
- `{"type":"table","columns":["Name","Value"],"rows":[["A",12],["B",24]]}`. Every row matches column count. Cells are strings, finite numbers, booleans, or null. Max 16 columns and 200 rows.
- `{"type":"steps","items":[{"title":"First step","description":"optional detail"}]}`
- `{"type":"tabs","items":[{"title":"Overview","blocks":[...supported blocks...]},{"title":"Details","blocks":[...]}]}`
- `{"type":"accordion","items":[{"title":"Assumptions","blocks":[...supported blocks...]}]}`

Max 32 blocks per list, 100 blocks total, nesting depth 4, 8 tabs/accordion sections, 12 metrics, 40 steps, and 128 KiB source. Keep interfaces small and useful. No actions, scripts, URLs, remote images, arbitrary styles, or HTML in preset data. The app renders the components with its active theme, compact controls, minimal lines, responsive spacing, and contained scrolling.

Example:

```aivory-ui
{"version":1,"title":"Monthly usage","blocks":[{"type":"metrics","items":[{"label":"Total tokens","value":3000}]},{"type":"chart","kind":"bar","labels":["June","July","August"],"series":[{"name":"Tokens","values":[800,1000,1200]}]}]}
```

## Interactive inline HTML

For a calculator, simulation, interactive diagram, or a custom view the presets cannot express, output a fenced `aivory-html` block. It renders directly inside the answer and updates while streaming. It is distinct from ordinary `html` code, which keeps the existing preview behavior.

Use one self-contained document with inline CSS and JavaScript. No imports, CDNs, remote assets, API requests, WebSockets, cookies, storage, frames, popups, forms submitting elsewhere, navigation, downloads, parent DOM, native desktop bridge, or authentication operations. The frame has an opaque origin and strict network isolation. Interactions stay inside the visualization; refreshes do not persist form state. Code is limited to 128 KiB. Code executes in a browser sandbox, not a server or Python environment.

Use CSS variables `--color-surface`, `--color-bg`, `--color-bg-muted`, `--color-fg`, `--color-fg-muted`, `--color-accent`, `--color-ring`. Design quietly: system fonts, 14px body, compact buttons and inputs with subtle fills, 8–12px corners, clear labels, visible keyboard focus, no decorative borders, no nested cards. Use responsive widths, no fixed desktop widths, and respect reduced motion. The viewport height adapts up to 900px, then scrolls internally. Escape user data as text, never executable markup.

Example:

```aivory-html
<style>label{display:block;margin-bottom:8px}input{width:100%;padding:8px;border:0;border-radius:8px;background:var(--color-bg-muted);color:var(--color-fg)}output{display:block;margin-top:16px;font-weight:600}</style>
<label for="quantity">Quantity</label><input id="quantity" type="range" min="1" max="100" value="10"><output id="result"></output>
<script>const quantity=document.getElementById('quantity');const result=document.getElementById('result');function update(){result.textContent='Total: $'+(Number(quantity.value)*12)}quantity.addEventListener('input',update);update()</script>
```

Do not wrap these fences in another code block. When you choose a visual or interactive answer, use `aivory-ui` or `aivory-html` even if the user did not explicitly request it; ordinary `json` or `html` will display code rather than the intended inline interface. Do not expose protocol details in the rendered title or body.
