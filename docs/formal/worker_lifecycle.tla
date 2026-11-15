------------------------- MODULE worker_lifecycle -------------------------
EXTENDS Naturals, FiniteSets, TLC

\* Source/action and property citations are in README.md. Two command slots,
\* two terminal addresses, two possible names, one external status change.
\* No transition-count constraint: terminal states stutter for temporal checks.
\*
\* The "reopen" mode (the Reopen* paths of Read, Admit, Submit, Retire) models
\* the internal helper cmdReopen (internal/cli/reopen.go:18). On the native
\* transport the CLI entry guard refuses `reopen` before that helper runs
\* (internal/cli/cli.go:256-258; the allowed list at cli.go:530-556 omits it),
\* so no native command reaches this scenario today. It is a model of that
\* helper, not a description of supported native behavior; see README.md.
\*
\* Report-based wait (ReportEventuallyObserved) holds only under the stated
\* assumption that the worker does not stay blocked forever: the Go wait has no
\* completion for a blocked terminal with a report (wait.go:472-518). README.md.
VARIABLE s
vars == <<s>>
IDs == 1..2
\* "reopen" is the internal-helper scenario described in the header: no native
\* command reaches it while the entry guard refuses `reopen`.
Modes == {"spawns", "release", "reopen", "wait"}
Ready == {"idle", "done"}
Live(t) == s.state[t] \notin {"absent", "closed"}
Named(n) == {t \in IDs : Live(t) /\ s.name[t] = n}
Target(n) == IF Named(n) = {} THEN 0 ELSE CHOOSE t \in Named(n): TRUE
Status(t) == IF t \in IDs THEN s.state[t] ELSE "gone"
RosterTarget(n) == LET rows == {t \in s.rows : s.name[t] = n} IN
                    IF rows = {} THEN 0 ELSE CHOOSE t \in rows: TRUE
InitialState(m, st, assigned, report, closes, forced, owned, active) ==
  [mode |-> m,
   state |-> [t \in IDs |-> IF t = 1 /\ m # "spawns" THEN st ELSE "absent"],
   name |-> [t \in IDs |-> IF t = 1 /\ m # "spawns" THEN 1 ELSE 0],
   rows |-> IF m = "spawns" THEN {} ELSE {1},
   pointer |-> assigned, report |-> report, active |-> active,
   kind |-> [i \in IDs |-> IF m = "spawns" THEN "spawn"
             ELSE IF m = "release" THEN IF i = 1 THEN "dispatch" ELSE "release"
             ELSE IF m = "reopen" THEN IF i = 1 THEN "reopen" ELSE "dispatch"
             ELSE IF i = 1 THEN "dispatch" ELSE "wait"],
   pc |-> [i \in IDs |-> IF m = "spawns" THEN "run" ELSE "read"],
   want |-> [i \in IDs |-> 1],
   target |-> [i \in IDs |-> 0],
   obs |-> [i \in IDs |-> "none"],
   hadTask |-> [i \in IDs |-> FALSE],
   hadReport |-> [i \in IDs |-> FALSE],
   outcome |-> [i \in IDs |-> "none"],
   close |-> closes, force |-> forced, owned |-> owned,
   admitted |-> FALSE, touchedOld |-> FALSE,
   blind |-> FALSE, badObserved |-> FALSE, badCore |-> FALSE,
   badRelease |-> FALSE, badOwner |-> FALSE, badWait |-> FALSE,
   badActiveRelease |-> FALSE, badActiveWait |-> FALSE,
   unsafeClose |-> FALSE, gap |-> FALSE,
   sent |-> FALSE, waitDone |-> FALSE, polls |-> 0,
   external |-> 1, attempts |-> [i \in IDs |-> 0]]

Init == \E m \in Modes, st \in {"idle", "working", "blocked", "done"},
           a \in BOOLEAN, r \in BOOLEAN, c \in BOOLEAN, f \in BOOLEAN,
           o \in BOOLEAN, active \in BOOLEAN:
          /\ (m \in {"spawns", "wait"} =>
               st = "idle" /\ ~a /\ ~r /\ c /\ ~f /\ o)
          /\ (m = "spawns" => ~active)
          /\ s = InitialState(m, st, a, r, c, f, o, active)

\* The allocation/name test is a Core contract assumption, not a Go lock.
Submit(i) ==
  /\ s.pc[i] = "run"
  /\ Named(s.want[i]) = {}                         \* MUTATE name-unique
  /\ LET t == IF s.kind[i] = "reopen" THEN 2 ELSE i IN
     /\ s.state[t] \in {"absent", "closed"}
     /\ s' = [s EXCEPT !.state[t] = "starting", !.name[t] = s.want[i],
                !.target[i] = t, !.pc[i] = "ack",
                !.attempts[i] = @ + 1]

NameConflict(i) ==
  /\ s.pc[i] = "run" /\ Named(s.want[i]) # {}
  /\ s' = [s EXCEPT !.want[i] = 2,
             !.pc[i] = IF s.want[i] = 1 THEN "run" ELSE "end"]

StartReply(i) ==
  /\ s.pc[i] = "ack"
  /\ \E result \in {"idle", "blocked", "unknown", "failed"}:
       s' = [s EXCEPT !.outcome[i] = result,
             !.state[s.target[i]] = IF result = "unknown" THEN "starting" ELSE result,
             !.pc[i] = IF result \in {"idle", "blocked"} THEN "prune" ELSE "cleanup"]

PruneStale(i) ==
  /\ s.pc[i] = "prune"
  /\ s' = [s EXCEPT !.rows = {t \in @ : s.name[t] # s.want[i]}, !.pc[i] = "publish"]

Publish(i) ==
  /\ s.pc[i] = "publish"
  /\ s' = [s EXCEPT !.rows = @ \cup {s.target[i]}, !.pc[i] = "end"]

PreserveUnknown(i) ==
  /\ s.pc[i] = "cleanup" /\ s.outcome[i] = "unknown"
  /\ s' = [s EXCEPT !.pc[i] = "end"]

CleanupFailed(i) ==
  /\ s.pc[i] = "cleanup"
  /\ s.outcome[i] = "failed"                       \* MUTATE uncertain-preserved
  /\ s' = [s EXCEPT !.state[s.target[i]] = "closed", !.pc[i] = "end",
             !.blind = @ \/ s.outcome[i] = "unknown"]

Read(i) ==
  /\ s.pc[i] = "read" /\ s.kind[i] # "wait"
  /\ IF RosterTarget(1) # 0
     THEN s' = [s EXCEPT !.target[i] = RosterTarget(1), !.obs[i] = Status(RosterTarget(1)),
               !.hadTask[i] = s.pointer, !.hadReport[i] = s.report,
               !.pc[i] = IF s.kind[i] = "dispatch" THEN "prepare"
                         ELSE IF s.kind[i] = "reopen" THEN "admit" ELSE "release-check"]
     ELSE s' = [s EXCEPT !.pc[i] = "end"]

Admit(i) ==
  /\ s.pc[i] = "admit"
  /\ \E ok \in BOOLEAN:
       s' = [s EXCEPT !.admitted = ok, !.pc[i] = "preflight-result"]

PreflightResult(i) ==
  /\ s.pc[i] = "preflight-result"
  /\ s.admitted                                    \* MUTATE reopen-preflight
  /\ s' = [s EXCEPT !.pc[i] = "release-check"]

PreflightRefused(i) ==
  /\ s.pc[i] = "preflight-result" /\ ~s.admitted
  /\ s' = [s EXCEPT !.pc[i] = "end"]

ObservedAllows(i) == ~(s.obs[i] \in {"working", "blocked"} /\ s.hadTask[i])
PrepareDispatch(i) ==
  /\ s.pc[i] = "prepare"
  /\ ObservedAllows(i)                              \* MUTATE dispatch-observed
  /\ s' = [s EXCEPT !.pointer = TRUE, !.report = FALSE,
             !.badObserved = @ \/ ~ObservedAllows(i), !.pc[i] = "send"]

DispatchRefused(i) ==
  /\ s.pc[i] = "prepare" /\ ~ObservedAllows(i)
  /\ s' = [s EXCEPT !.pc[i] = "end"]

\* The Core guard is checked at the write, after the earlier Go read.
Send(i) ==
  /\ s.pc[i] \in {"send", "send-retry"} /\ Target(1) # 0
  /\ LET t == Target(1) IN
     /\ s.state[t] \in Ready                        \* MUTATE core-send-guard
     /\ s' = [s EXCEPT !.state[t] = "working", !.sent = TRUE,
                !.badCore = @ \/ s.state[t] \notin Ready, !.pc[i] = "end"]

SendBusy(i) ==
  /\ s.pc[i] = "send" /\ Target(1) # 0
  /\ Status(Target(1)) = "working"
  /\ s' = [s EXCEPT !.pc[i] = "retry"]

RetryOnce(i) ==
  /\ s.pc[i] = "retry"
  /\ IF Target(1) # 0 /\ Status(Target(1)) \in Ready
     THEN s' = [s EXCEPT !.pc[i] = "send-retry"]
     ELSE s' = [s EXCEPT !.pc[i] = "end"]

\* Send above is the same backend guard at both call sites. No third send.
RetryRefused(i) ==
  /\ s.pc[i] = "send-retry"
  /\ Status(Target(1)) \notin Ready
  /\ s' = [s EXCEPT !.pc[i] = "end"]

SendGoneOrBlocked(i) ==
  /\ s.pc[i] = "send"
  /\ (Target(1) = 0 \/ Status(Target(1)) \notin Ready \cup {"working"})
  /\ s' = [s EXCEPT !.pc[i] = "end"]

Closes(i) == s.kind[i] = "reopen" \/ s.close
ReleaseAllows(i) == s.force \/ s.hadReport[i] \/ ~Closes(i)
                     \/ ~s.hadTask[i] \/ s.obs[i] # "working"
ReleaseCheck(i) ==
  /\ s.pc[i] = "release-check"
  /\ ~s.active                                     \* MUTATE active-release
  /\ ReleaseAllows(i)                               \* MUTATE release-observed
  /\ s' = [s EXCEPT !.pc[i] = IF Closes(i) THEN "close" ELSE "retire",
             !.badRelease = @ \/ ~ReleaseAllows(i), !.badActiveRelease = @ \/ s.active]

ReleaseRefused(i) ==
  /\ s.pc[i] = "release-check" /\ (~ReleaseAllows(i) \/ s.active)
  /\ s' = [s EXCEPT !.pc[i] = "end"]

Close(i) ==
  /\ s.pc[i] = "close"
  /\ s.owned                                       \* MUTATE owned-close
  /\ s' = [s EXCEPT !.state[s.target[i]] = "closed", !.pc[i] = "retire",
             !.badOwner = @ \/ ~s.owned,
             !.touchedOld = @ \/ s.kind[i] = "reopen",
             !.unsafeClose = @ \/ (~s.force /\ s.state[s.target[i]] = "working"
                                      /\ s.pointer /\ ~s.report)]

CloseRefused(i) ==
  /\ s.pc[i] = "close" /\ ~s.owned
  /\ s' = [s EXCEPT !.pc[i] = "end"]

Retire(i) ==
  /\ s.pc[i] = "retire"
  /\ s' = [s EXCEPT !.rows = {t \in @ : s.name[t] # 1},
             !.pointer = FALSE, !.report = FALSE,
             !.gap = @ \/ s.kind[i] = "reopen",
             !.pc[i] = IF s.kind[i] = "reopen" THEN "run" ELSE "end"]

\* One unsolicited Core status update can interleave with every Go action.
CoreChange(t) ==
  /\ s.external > 0 /\ Live(t) /\ s.state[t] # "starting"
  /\ \E st \in {"idle", "working", "blocked", "done"} \ {s.state[t]}:
       s' = [s EXCEPT !.state[t] = st, !.external = @ - 1]

WriteReport ==
  /\ s.sent /\ s.pointer /\ ~s.report
  /\ s' = [s EXCEPT !.report = TRUE]

\* External orchestrator explicitly finalizes a valid assignment; evidence,
\* verdict selection and round validation are not modelled here.
Finalize ==
  /\ s.active /\ s.report
  /\ s' = [s EXCEPT !.active = FALSE]

\* Two stable observations abstract report identity plus the finite working
\* timeout; the report is assumed stable, the clock advances, no new task.
\* A blocked terminal does not complete the wait: reportAgentState has no
\* blocked case and returns pending (internal/wait/wait.go:472-518), so polling
\* makes no progress until the worker leaves the blocked state or the overall
\* wait timeout ends the command (that timeout is not modelled).
Blocked == Status(Target(1)) = "blocked"
WaitPoll ==
  /\ s.mode = "wait" /\ ~s.waitDone
  /\ ~Blocked
  /\ ~s.active                                     \* MUTATE active-wait
  /\ s.report                                      \* MUTATE wait-report
  /\ s' = [s EXCEPT !.polls = IF @ < 2 THEN @ + 1 ELSE @,
             !.waitDone = s.polls = 2,
             !.badWait = @ \/ (s.polls = 2 /\ ~s.report),
             !.badActiveWait = @ \/ (s.polls = 2 /\ s.active)]

Command(i) == Submit(i) \/ NameConflict(i) \/ StartReply(i) \/ PruneStale(i) \/ Publish(i)
  \/ PreserveUnknown(i) \/ CleanupFailed(i) \/ Read(i) \/ Admit(i)
  \/ PreflightResult(i) \/ PreflightRefused(i) \/ PrepareDispatch(i)
  \/ DispatchRefused(i) \/ Send(i) \/ SendBusy(i) \/ RetryOnce(i)
  \/ RetryRefused(i) \/ SendGoneOrBlocked(i) \/ ReleaseCheck(i)
  \/ ReleaseRefused(i) \/ Close(i) \/ CloseRefused(i) \/ Retire(i)
Next == (\E i \in IDs: Command(i)) \/ (\E t \in IDs: CoreChange(t))
          \/ WriteReport \/ Finalize \/ WaitPoll
Spec == Init /\ [][Next]_vars
          /\ WF_vars(WaitPoll)                      \* MUTATE wait-fairness

UncertainPreserved == ~s.blind
UniqueLiveRoster == \A a,b \in s.rows:
  (a # b /\ Live(a) /\ Live(b)) => s.name[a] # s.name[b]
DispatchObservedGuard == ~s.badObserved
CoreSendGuard == ~s.badCore
ReleaseObservedGuard == ~s.badRelease
OwnedCloseOnly == ~s.badOwner
ReopenPreflight == s.touchedOld => s.admitted
WaitHasReport == ~s.badWait
ActiveAssignmentOwnsCompletion == ~s.badActiveWait /\ ~s.badActiveRelease
\* Assumption: the worker eventually leaves the blocked state, that is, it is
\* not blocked forever. A worker that stays blocked leaves the wait pending, so
\* the property is stated only for behaviors that satisfy it.
EventuallyUnblocked == []<>(~Blocked)
ReportEventuallyObserved == s.mode = "wait" =>
  (EventuallyUnblocked => ((s.sent /\ s.report /\ ~s.active) ~> s.waitDone))

\* Diagnostic expectations, deliberately absent from the safety config.
\* See README: one is a real race, the other is explicitly accepted in Go.
NoUnreportedWorkingClose == ~s.unsafeClose
\* Diagnostic: the same liveness without the unblocked assumption fails on a
\* worker that stays blocked after its report. Run with
\* `PROPERTY ReportEventuallyObservedUnconditional` in place of the property
\* line of the config; it is not part of the safety configuration.
ReportEventuallyObservedUnconditional == s.mode = "wait" =>
  ((s.sent /\ s.report /\ ~s.active) ~> s.waitDone)
NoReopenGap == ~s.gap
NoCoreChange == s.external = 1
CloseRaceInit == Init /\ s.mode = "release" /\ s.state[1] = "idle"
  /\ s.pointer /\ ~s.report /\ s.close /\ ~s.force /\ s.owned /\ ~s.active
CloseRaceSpec == CloseRaceInit /\ [][Next]_vars
=============================================================================
