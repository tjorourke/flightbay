# Ambient follow-up tasks

Status: **Deferred. Saved for a separate investigation and implementation pass.**

The current request is limited to clear Bookinfo installation progress and completion feedback. Do not treat the items below as approval to implement the broader Ambient changes now.

Feedback context: “The last two days of Ambient work are the right product. These are the remaining ways it can still be misread.”

- [ ] **1. Make HTTP results lead with the connection that was actually tested.**

  The HTTP result leads with the name it did not test. HTTP 200 7 ms global-echo…mesh.internal:8080 is a port-forward to one Pod with that string as the Host header. Cluster DNS and global DNS are the same connection, drawn as two successes. Lead with "backend Pod only, mesh path not taken." A 200 on the global name must not look like east-west worked.

- [ ] **2. Separate dry-run validation from applied, pending and failed state across the review page.**

  Dry-run passed is the same green as done. The review page shows three Apply buttons, and each row already looks finished. After a partial apply there is no applied / pending / failed strip. A later failure leaves the first cluster changed. That state has to be the page, not a sentence under the buttons.

- [ ] **3. Draw attachment relationships on the canvas without dropping overlapping relationships.**

  The new lines answer peering, not attachment. The canvas draws peer and global-publication lines, then hides routes, weights, and waypoints in a collapsed details block. amRoute drops a line and tells you to drag cards apart. The hooked teal line in the screenshot reads as if global-echo is on the peer path. Draw attachment on the canvas. Never omit a relationship because two cards overlap.

- [ ] **4. Make Needs attention the landing experience and default investigations to actionable checks.**

  Failures were added under a configure launcher. Landing still opens on "Explore your ambient mesh," and New configuration is the primary button. Status filters exist, but a resource opens on All, and the screenshot is sitting on Pass while Check and Unknown are unselected. Open on Failed, Check, and Unknown. Needs attention is the page.

- [ ] **5. Make endpoint recovery show selector membership and use the evidence already collected.**

  The endpoint table hides the selector. Eight pods are Ready. The two that match are at the bottom, and the recovery action is still "Collect missing evidence" after collection is complete. Split in-selector and not-in-selector. Do not offer a collect command when the evidence is already on screen.

- [ ] **6. Lock context selection to the cluster shown on the card.**

  The context dropdown can disagree with the card. A row titled kind-mesh1 also has a context select that can point at another API server. HTTP test asks again. Lock the select to that cluster. A mismatch is a block, not a choice.

- [ ] **7. Put the edition-specific policy kind in the review title.**

  Edition is still a disclosure. Whether the plan writes AgentgatewayPolicy or EnterpriseAgentgatewayPolicy is inside Implementation details. That belongs in the review title. It is the thing you are about to apply.

- [ ] **8. Reuse a single DNS state chip rather than repeating names without a verdict.**

  DNS is four widgets and no verdict. The card, the target-bar menu, the purple line, and the HTTP authority all show the same hostname, none with resolves, not tested, or route hit. One chip, one state, reused everywhere.

- [ ] **9. Explain the combined object count and waypoint prerequisite before Configure.**

  Combined tasks have no combined preview. Deny, mutual TLS, and a split can be checked together, then become a long form. On the task step, say "this writes N objects" before Configure. If the waypoint is missing, Waypoint setup is the next step, not a footer button.

- [ ] **10. Make the transition from read-only investigation to live configuration explicit.**

  Configure is one tab from a read-only investigation, on a live mirror. The apply modal is careful. The entry is not. Demote New configuration. Mark Configure as "writes to the cluster" when the workspace is live. Stepper checkmarks must not read as safe to apply.
