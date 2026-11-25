# Delivery metrics pilot

Use one record per delivery episode. A demand ID identifies the requested change, a change ID identifies the integrated revision or review, and a publication ID identifies the release or deployment available to the intended audience. Keep the IDs source-qualified and carry a stable receipt link or ID for each observed event. Record gaps explicitly. The Go package in `internal/deliverymetrics` accepts evidence already collected from its sources.

## Landmarks and coverage

Record timestamps in UTC and preserve the source's precision. A tracker creation event can establish the request timestamp. Use an explicit start transition or a contemporaneous start receipt for start. A first commit or PR opening can bound when work was already underway. Use the merge event and the publication event for the last two landmarks, and verify the released revision contains the change. Use publication time when a release is created earlier than it becomes available.

| Measure | Calculation |
| --- | --- |
| Lead time | Published minus requested |
| Cycle time | Published minus started |
| Request to start | Started minus requested |
| Start to merge | Merged minus started |
| Merge to release | Published minus merged |
| Observed active time | Length of the union of implementation, review, validation, rework and CI runtime intervals |
| Observed waiting time | Length of the union of evidenced waiting and CI queue intervals |
| Unclassified cycle time | Cycle time minus the union of active and waiting intervals clipped to the cycle |

Both landmarks are required for each elapsed span. Equal instants give a duration of zero. Contradictory timestamps return an error. An open item can have prepublication spans. Canceled and reopened records keep their observed windows and coverage. Calculate completed spans only for a verified delivery episode, with a new episode after reopening when the evidence identifies it.

Intervals use half-open bounds in elapsed calendar time. For CI jobs, use the source's queue timestamp, actual start and actual completion. For example, if the queue proxy is GitHub Actions job creation, label it `job created → started` and leave any earlier scheduling gap unclassified. Queue and runtime are separate unions. Their windows can overlap across jobs, so report each alongside its own coverage. Waiting records may name review, decision, dependency, changed scope, environment or release only when a source supports the cause.

Deduplicate observations by receipt ID. For CI, use source, run, attempt and job. A rerun is a new attempt. Conflicting payloads for the same identity are evidence errors. The union prevents parallel jobs and repeated windows from inflating calendar time. Window totals describe observed coverage, and they can extend outside the cycle. The unclassified calculation always clips to the cycle and counts overlapping active and waiting windows once. Unclassified time has no assigned cause.

Report the known landmarks out of four, coverage per window type, the number of unique CI jobs, the number with sufficient queue and runtime timestamps, and any known missing sources. If a planned check has no surviving receipt, record it as unobserved. Keep raw evidence in a location with access appropriate to its contents. Report times with units and with precision appropriate to the least precise endpoint.

## Intake profile

Record functional size as the inventory of observable flows with entry, exit, read and write counts for each flow. For example, if an existing export flow accepts a filter, reads records and emits a file, record one entry, one read, one exit and zero writes for that flow. Record the technical profile and uncertainty independently.

| Rubric v1 dimension | 0 | 1 | 2 |
| --- | --- | --- | --- |
| Interfaces and dependencies | Local change | One contract or dependency | Several contracts, systems or dependencies |
| State and migration | Existing state stays compatible and unchanged | New state or compatible edit | Migration, concurrency or difficult rollback |
| Risk | Local, reversible failure | Affects a significant flow | Security exposure, data loss or broad impact |
| Planned validation | One layer on one surface | Several layers or surfaces | Platform matrix or integration and failure scenarios |
| Uncertainty | Requirements and mechanism known | Bounded gaps | Open discovery or external dependency |

Keep the rating and a short justification for each dimension. Leave an unsupported score unknown. For a genuine intake assessment, record the sources and what was known before work began. For a reconstructed assessment, label it retrospective, describe which information can be dated to intake, and leave undated dimensions unknown. Store subsequent assessments as separate revisions with the reason for the change. This pilot keeps the individual dimensions.

Record the intended validation scenarios, layers and platforms when available. Compare them at close with receipts for observed scenarios, results, attempts, runtimes and exact revisions. Treat counts as descriptive and retain gaps between planned and observed evidence.

## Reading the pilot

For a reproducible summary, show the linked sources, each supported elapsed span, observed windows by stage, overlapping work, any evidenced waits, and missing coverage. Compare completed episodes in cohorts with similar demand types, functional flows and technical profiles. Include sample size and cycle coverage. One released example shows how to reconstruct evidence and yields insufficient data for a forecast. Once a cohort supports a forecast, estimate the median and 80th percentile of cycle time and check the error and interval coverage on later deliveries using their intake profiles.

The package measures one record at a time. Prospective intake capture, comparison between independent assessors and evaluation of forecasts need additional pilot deliveries.
