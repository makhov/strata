------------------------------ MODULE Election ------------------------------
EXTENDS Naturals, TLC

\* A first, intentionally small model of T4's S3 leader-lock protocol.
\*
\* Scope:
\* - S3 conditional writes are modeled as atomic state transitions.
\* - committedRev is the election fence advertised in leader-lock.
\* - applied[n] is the highest committed revision node n can safely serve.
\*
\* Out of scope for this first model:
\* - Pebble internals
\* - gRPC stream mechanics
\* - object-store WAL/checkpoint recovery
\* - wall-clock drift beyond a bounded logical clock

CONSTANTS Nodes, MaxRev, MaxTerm, TTL, MaxTime

None == "none"

VARIABLES owner, term, lastSeen, committedRev, applied, accepting, now, prevTerm

Vars == <<owner, term, lastSeen, committedRev, applied, accepting, now, prevTerm>>

TypeOK ==
  /\ Nodes # {}
  /\ owner \in Nodes \cup {None}
  /\ term \in 0..MaxTerm
  /\ prevTerm \in 0..MaxTerm
  /\ lastSeen \in 0..MaxTime
  /\ committedRev \in 0..MaxRev
  /\ applied \in [Nodes -> 0..MaxRev]
  /\ accepting \in [Nodes -> BOOLEAN]
  /\ now \in 0..MaxTime

Init ==
  /\ owner = None
  /\ term = 0
  /\ prevTerm = 0
  /\ lastSeen = 0
  /\ committedRev = 0
  /\ applied = [n \in Nodes |-> 0]
  /\ accepting = [n \in Nodes |-> FALSE]
  /\ now = 0

BumpTerm ==
  /\ term < MaxTerm
  /\ prevTerm' = term
  /\ term' = term + 1

KeepTerm ==
  /\ prevTerm' = term
  /\ term' = term

TryAcquire(n) ==
  /\ owner \in {None, n}
  /\ BumpTerm
  /\ owner' = n
  /\ lastSeen' = now
  /\ committedRev' = applied[n]
  /\ accepting' = [x \in Nodes |-> x = n]
  /\ UNCHANGED <<applied, now>>

Touch(n) ==
  /\ owner = n
  /\ accepting[n]
  /\ KeepTerm
  /\ lastSeen' = now
  /\ committedRev' = applied[n]
  /\ UNCHANGED <<owner, applied, accepting, now>>

CommitWrite(n) ==
  /\ owner = n
  /\ accepting[n]
  /\ applied[n] < MaxRev
  /\ KeepTerm
  /\ applied' = [applied EXCEPT ![n] = @ + 1]
  /\ committedRev' = applied[n] + 1
  /\ lastSeen' = now
  /\ UNCHANGED <<owner, accepting, now>>

CatchUp(n) ==
  /\ owner # None
  /\ n # owner
  /\ applied[n] < committedRev
  /\ KeepTerm
  /\ applied' = [applied EXCEPT ![n] = @ + 1]
  /\ UNCHANGED <<owner, lastSeen, committedRev, accepting, now>>

TakeOver(n) ==
  /\ owner # None
  /\ owner # n
  /\ now >= lastSeen + TTL
  /\ committedRev <= applied[n]
  /\ BumpTerm
  /\ owner' = n
  /\ lastSeen' = now
  /\ committedRev' = applied[n]
  /\ accepting' = [x \in Nodes |-> x = n]
  /\ UNCHANGED <<applied, now>>

StepDown(n) ==
  /\ accepting[n]
  /\ owner # n
  /\ KeepTerm
  /\ accepting' = [accepting EXCEPT ![n] = FALSE]
  /\ UNCHANGED <<owner, lastSeen, committedRev, applied, now>>

Tick ==
  /\ now < MaxTime
  /\ KeepTerm
  /\ now' = now + 1
  /\ UNCHANGED <<owner, lastSeen, committedRev, applied, accepting>>

Next ==
  \/ \E n \in Nodes: TryAcquire(n)
  \/ \E n \in Nodes: Touch(n)
  \/ \E n \in Nodes: CommitWrite(n)
  \/ \E n \in Nodes: CatchUp(n)
  \/ \E n \in Nodes: TakeOver(n)
  \/ \E n \in Nodes: StepDown(n)
  \/ Tick

Spec == Init /\ [][Next]_Vars

SingleAcceptingLeader ==
  \A a, b \in Nodes:
    (accepting[a] /\ accepting[b]) => a = b

AcceptingLeaderOwnsLock ==
  \A n \in Nodes:
    accepting[n] => owner = n

LeaderHasCommittedFence ==
  owner = None \/ applied[owner] >= committedRev

TermNeverRegresses ==
  term >= prevTerm

=============================================================================
