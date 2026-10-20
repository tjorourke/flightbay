# Flightbay agent instructions

The plan for the Ambient rebuild is `docs/ux/ambient/PLAN.md`. Read §0 completely before any Ambient UI work.

## Mockup fidelity contract (PLAN.md §0.5)

Earlier hand-offs drifted away from the mockups. These rules exist to stop that. They apply to every task that renders UI.

1. **The mockups are the spec.** Order of authority: mockup HTML source, then this plan's text, then existing Flightbay code and CSS. If the plan text and a mockup disagree, stop and ask Tom. Do not pick one quietly.
2. **Read the HTML, not only the PNG.** For each screen, open `docs/ux/ambient/<screen>.html` (the source) and view `<screen>.png`. The HTML has the exact wording, element order and CSS classes. Re-open both at the start of every screen and after any context compaction. Do not work from memory.
3. **Build the new markup; reuse the old logic.** Port `docs/ux/ambient/mock.css` into `web/ambient-v2.css` (prefix classes `am2-`, map colours onto the tokens in `web/app.css`) and keep the mockup's structure: the same containers, the same order, the same element types (segmented control, card, text link, pill). Reuse existing JS for data, plans and applies. Do **not** wrap new data in old components such as `.panel`, `.am-*` cards or the old stepper because they are already there.
4. **Copy the text exactly.** Headings, tabs, buttons, links, column headers, chips, toggles and status words are copied verbatim from the mockup. Data (Service names, numbers, rule summaries) comes from the API and will differ. That is expected.
5. **Add nothing and drop nothing.** No extra buttons, badges, columns, helper text, icons or explainer panels that are not in the mockup. If you think something is missing, finish the screen as drawn and list the suggestion in the PR description.
6. **Ignore the annotations.** Yellow numbered circles and the yellow "notes" boxes explain the design. Never build them.
7. **Verify every screen with the comparison loop.** It is repeated until it passes:
   1. Start the preview from source:
      `FLIGHTBAY_AMBIENT_PREVIEW=$PWD/flightbay-mesh1-objects.tar.zst go test ./internal/server -run TestAmbientBrowserPreview -v -timeout 20m`
      (it serves `web/` from disk, disables real cluster actions and prints `AMBIENT_PREVIEW=<url>`).
   2. Set the routes in `docs/ux/ambient/tools/routes.json` to objects that exist in the fixture.
   3. Run `python3 docs/ux/ambient/tools/ux_compare.py --base '<url without #…>' --screen <screen>`.
   4. Open `ux-review/<screen>/report.md` and **view** `ux-review/<screen>/side-by-side.png` (mockup on the left, app on the right).
   5. Write down every visible difference that is not data: missing or extra elements, wrong order, wrong wording, wrong control type, layout, spacing, visual weight (for example a table where the mockup has cards, or a primary button where the mockup has a text link).
   6. Fix them and go back to step 3. Stop only when the report has no `MUST` failures and the only differences left in the image are data.
8. **Get an independent review.** When the loop passes, start a fresh sub-agent with no project context. Give it only the mockup PNG, `ux-review/<screen>/app.png` and `ux-specs/<screen>.json`, and ask: "List every difference between these two screens other than the data values. Be strict." Fix what it finds, then rerun the loop.
9. **One screen at a time, with a gate.** Finish, verify and commit one screen before starting the next. Put the side-by-side PNG and the final report in the commit or PR description, and pause for Tom's approval before moving to the next screen.
10. **Record deviations.** If a difference must stay (a technical limit, or Tom agreed to it), list it in `docs/ux/ambient/DEVIATIONS.md` with the screen, the difference and the reason. An unlisted difference is a defect.

`ux-specs/*.json` is generated from the mockups by `tools/extract_specs.py`. Do not hand-edit it. If a mockup changes, rerun the extractor. The text check catches missing and reordered labels; it cannot see layout or extra elements, which is why steps 7.4 to 7.5 and 8 are mandatory.
