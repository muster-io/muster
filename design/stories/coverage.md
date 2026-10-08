# Coverage of L1 requirements by stories

- Status: Draft
- Date: 2026-10-04

Which story covers each functional requirement (`C-NN.FR-n`) and acceptance statement (`C-NN.AC-n`) of
[L1](../prd/L1.md). A capability is done when its stories together cover every ID of its file
([conventions](README.md#3-sizing-and-slicing)). The table of each story file says whether it covers an ID fully or in
part; this page joins them.

- **Covered by** lists the written stories that cover the ID, with `(part)` where a story covers only part of it.
- **Status**: `complete` — the listed stories cover the ID together; `open: S-NNN` — a planned story completes it.
  Every ID of L1 is complete.
- **Note** names, for an acceptance statement that cannot be checked when its capability merges, the later story that
  verifies it — the one in which its dependency appears — and why ([L1 conventions](../prd/L1.md#2-conventions)).

Contents: [C-01](#c-01) · [C-02](#c-02) · [C-03](#c-03) · [C-04](#c-04) · [C-05](#c-05) · [C-06](#c-06) · [C-07](#c-07) · [C-08](#c-08) · [C-09](#c-09) · [C-10](#c-10) · [C-11](#c-11) · [C-12](#c-12) · [C-13](#c-13) · [C-14](#c-14) · [C-15](#c-15) · [C-16](#c-16) · [C-17](#c-17) · [C-18](#c-18) · [C-19](#c-19) · [C-20](#c-20) · [C-21](#c-21) · [NFR](#non-functional-requirements)

## Summary

| Capability | Phase | IDs | Stories | Contracts written | Complete |
|---|---|---|---|---|---|
| C-01 Project skeleton and releases | Foundation | 20 | S-001, S-002, S-003, S-004 | yes | 20 of 20 |
| C-02 Process startup and runtime | Foundation | 36 | S-005, S-006, S-007, S-008, S-009 | yes | 36 of 36 |
| C-03 Sign-in and access | Foundation | 58 | S-010, S-011, S-012, S-013, S-062, S-014, S-015 | yes | 58 of 58 |
| C-04 API tokens | Foundation | 17 | S-016, S-017 | yes | 17 of 17 |
| C-05 Integrations | Observation | 18 | S-018, S-019 | yes | 18 of 18 |
| C-06 Snapshot processing | Observation | 36 | S-020, S-021, S-022, S-065 | yes | 36 of 36 |
| C-07 Heartbeat | Observation | 12 | S-023, S-024 | yes | 12 of 12 |
| C-08 Routing | Observation | 22 | S-025, S-026, S-027 | yes | 22 of 22 |
| C-09 Alert Group lifecycle | Observation | 49 | S-028, S-029, S-030, S-031, S-065 | yes | 49 of 49 |
| C-10 Commands | Observation | 35 | S-032, S-063, S-033 | yes | 35 of 35 |
| C-11 Delivery engine | Shadow | 35 | S-034, S-035 | yes | 35 of 35 |
| C-12 Messages | Shadow | 19 | S-036, S-037, S-038 | yes | 19 of 19 |
| C-13 Mattermost | Shadow | 28 | S-039, S-061, S-040, S-064 | yes | 28 of 28 |
| C-14 Telegram | Shadow | 35 | S-041, S-042, S-043 | yes | 35 of 35 |
| C-15 Outgoing webhook | Shadow | 25 | S-044, S-045, S-046 | yes | 25 of 25 |
| C-16 Destination test | Shadow | 14 | S-047, S-048 | yes | 14 of 14 |
| C-17 Timers | Actions | 20 | S-049, S-050 | yes | 20 of 20 |
| C-18 Account links | Actions | 18 | S-051, S-052 | yes | 18 of 18 |
| C-19 Self-observation | Operations | 15 | S-053, S-054, S-059 | yes | 15 of 15 |
| C-20 Organization settings and security | Operations | 12 | S-055, S-056 | yes | 12 of 12 |
| C-21 Documentation site and runbooks | Operations | 9 | S-057, S-058, S-060 | yes | 9 of 9 |

## C-01

[Project skeleton and releases](../prd/l1/C-01-project-skeleton-and-releases.md) · Foundation · stories: S-001, S-002, S-003, S-004

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-01.FR-1 | S-001 (part), S-003 (part) | complete |  |
| C-01.FR-2 | S-001 | complete |  |
| C-01.FR-3 | S-001 | complete |  |
| C-01.FR-4 | S-002 | complete |  |
| C-01.FR-5 | S-001 (part), S-002 (part), S-007 (part) | complete | the binary links the SPA when the app listener serves it, in S-006 |
| C-01.FR-6 | S-001 | complete |  |
| C-01.FR-7 | S-001 (part), S-002 (part), S-004 (part), S-061 (part) | complete | the load test's full profile and thresholds come with delivery to a messenger, in S-061 |
| C-01.FR-8 | S-001 | complete |  |
| C-01.FR-9 | S-003 | complete |  |
| C-01.FR-10 | S-003 | complete |  |
| C-01.FR-11 | S-002 (part), S-003 (part) | complete |  |
| C-01.FR-12 | S-001 (part), S-005 (part), S-006 | complete |  |
| C-01.FR-13 | S-004 (part), S-006 (part), S-009 (part), S-013 (part), S-018 (part), S-020 (part), S-023 (part), S-037 (part), S-039 (part), S-041 (part), S-042 (part), S-044 (part), S-045 (part), S-047 (part) | complete | later capabilities extend their fake servers and the demo configuration |
| C-01.FR-14 | S-001, S-058 (part) | complete | the README demo animation is added after the frontend stories, with S-058 |
| C-01.FR-15 | S-003 (part), S-004 (part) | complete |  |
| C-01.AC-1 | S-001 (part), S-002 (part) | complete |  |
| C-01.AC-2 | S-003, S-006 | complete | verified by S-006: the installed chart becomes ready only once migrations, probes and serving exist |
| C-01.AC-3 | S-003 | complete |  |
| C-01.AC-4 | S-004 | complete |  |
| C-01.AC-5 | S-003 | complete |  |

## C-02

[Process startup and runtime](../prd/l1/C-02-process-startup-and-runtime.md) · Foundation · stories: S-005, S-006, S-007, S-008, S-009

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-02.FR-1 | S-006, S-009 (part) | complete |  |
| C-02.FR-2 | S-006 | complete |  |
| C-02.FR-3 | S-006 | complete |  |
| C-02.FR-4 | S-006 | complete |  |
| C-02.FR-5 | S-006 | complete |  |
| C-02.FR-6 | S-006 | complete |  |
| C-02.FR-7 | S-007 | complete |  |
| C-02.FR-8 | S-007 | complete |  |
| C-02.FR-10 | S-008 (part), S-023 (part), S-041 (part), S-053 (part) | complete | later Leader tasks join with S-023, S-041 and S-053 |
| C-02.FR-11 | S-008 | complete |  |
| C-02.FR-12 | S-008 (part), S-028 (part), S-049 (part) | complete | collapsed overdue timers and Timeline entries are C-09 and C-17 (S-028, S-049) |
| C-02.FR-13 | S-008 | complete |  |
| C-02.FR-14 | S-006 (part), S-008 (part), S-061 (part), S-041 (part), S-042 (part) | complete | Connection and Destination checks join with S-061, S-041 and S-042 |
| C-02.FR-15 | S-006 (part), S-008 (part), S-011 (part), S-012 (part), S-021 (part), S-055 (part) | complete | the other subcommands come with S-011, S-012, S-021 and S-055 |
| C-02.FR-16 | S-006 | complete |  |
| C-02.FR-17 | S-005 | complete |  |
| C-02.FR-18 | S-005 | complete |  |
| C-02.FR-19 | S-005 (part), S-006 (part), S-008 (part), S-009 (part), S-010 (part) | complete |  |
| C-02.FR-20 | S-009 | complete |  |
| C-02.FR-21 | S-009, S-055 (part) | complete | editing the policy, effective at once, is S-055 |
| C-02.FR-22 | S-009 (part), S-013 (part), S-015 (part) | complete |  |
| C-02.FR-23 | S-007 | complete |  |
| C-02.FR-24 | S-008, S-012 | complete |  |
| C-02.AC-1 | S-007 | complete |  |
| C-02.AC-2 | S-006 | complete |  |
| C-02.AC-3 | S-008 | complete |  |
| C-02.AC-4 | S-008 | complete |  |
| C-02.AC-5 | S-006 | complete |  |
| C-02.AC-6 | S-007 | complete |  |
| C-02.AC-7 | S-006 | complete |  |
| C-02.AC-8 | S-006 (part), S-018 | complete | verified live by S-018: the ingestion route arrives with C-05; S-006 checks the shared port with stub handlers |
| C-02.AC-9 | S-009 | complete |  |
| C-02.AC-10 | S-009 | complete |  |
| C-02.AC-11 | S-009 | complete |  |
| C-02.AC-12 | S-005 | complete |  |
| C-02.AC-13 | S-008 | complete |  |

## C-03

[Sign-in and access](../prd/l1/C-03-sign-in-and-access.md) · Foundation · stories: S-010, S-011, S-012, S-013, S-062, S-014, S-015

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-03.FR-1 | S-010 | complete |  |
| C-03.FR-2 | S-010 | complete |  |
| C-03.FR-3 | S-010 (part), S-011 (part), S-015 | complete |  |
| C-03.FR-4 | S-010 | complete |  |
| C-03.FR-5 | S-013 (part), S-015 | complete |  |
| C-03.FR-6 | S-013 (part), S-015 | complete |  |
| C-03.FR-7 | S-013 (part), S-014 | complete |  |
| C-03.FR-8 | S-013 (part), S-015 | complete |  |
| C-03.FR-9 | S-010 (part), S-011 (part), S-062 (part) | complete |  |
| C-03.FR-10 | S-012 (part), S-013 (part), S-014 | complete |  |
| C-03.FR-11 | S-011 (part), S-012 (part), S-062 (part), S-015 | complete |  |
| C-03.FR-12 | S-010 (part), S-014 (part), S-017 (part), S-052 (part) | complete | Personal access tokens join with S-017, Account links with S-052 |
| C-03.FR-13 | S-011 (part), S-015 (part), S-016 (part), S-063 (part), S-049 (part), S-051 (part) | complete | the effects on Account links come with S-051; the release of the acknowledgements of disabled and deleted users with S-063 and S-049 |
| C-03.FR-14 | S-010 (part), S-011 (part), S-012 (part), S-013 (part), S-062 (part), S-051 (part) | complete | each later capability adds its entry types; Account links with S-051 |
| C-03.FR-15 | S-011 (part), S-015 | complete |  |
| C-03.FR-16 | S-010 | complete |  |
| C-03.FR-17 | S-013 | complete |  |
| C-03.FR-18 | S-012 (part), S-014, S-015 (part) | complete |  |
| C-03.FR-19 | S-013 (part), S-015 | complete |  |
| C-03.FR-20 | S-012, S-015 | complete |  |
| C-03.FR-21 | S-013 (part), S-015 | complete |  |
| C-03.FR-22 | S-010 | complete |  |
| C-03.FR-23 | S-010 | complete |  |
| C-03.FR-24 | S-010 (part), S-012 (part), S-013 (part), S-014 | complete |  |
| C-03.FR-25 | S-013 (part), S-014 | complete |  |
| C-03.FR-26 | S-011 (part), S-014 | complete |  |
| C-03.FR-27 | S-010 (part), S-012 (part), S-016 | complete |  |
| C-03.FR-28 | S-013 (part), S-014 | complete |  |
| C-03.FR-29 | S-062 (part), S-014 (part), S-015 | complete |  |
| C-03.FR-30 | S-013 (part), S-062 (part), S-016 (part) | complete |  |
| C-03.FR-31 | S-011 | complete |  |
| C-03.FR-32 | S-013 (part), S-062 (part), S-015 | complete |  |
| C-03.AC-1 | S-013 | complete |  |
| C-03.AC-2 | S-014 | complete |  |
| C-03.AC-3 | S-010 | complete |  |
| C-03.AC-4 | S-011 | complete |  |
| C-03.AC-6 | S-013 | complete |  |
| C-03.AC-7 | S-013 (part), S-015 | complete |  |
| C-03.AC-8 | S-013 | complete |  |
| C-03.AC-9 | S-014 | complete | the live-updates stream it needs is C-09.FR-25, brought forward into S-012; S-014 verifies the banner |
| C-03.AC-10 | S-010 | complete |  |
| C-03.AC-11 | S-010, S-011 | complete |  |
| C-03.AC-12 | S-010 | complete |  |
| C-03.AC-13 | S-012 (part), S-013 | complete |  |
| C-03.AC-14 | S-016 (part), S-051 | complete | verified by later stories: the token parts need C-04 (S-016), removing an Account link needs C-18 (S-051) |
| C-03.AC-15 | S-012 | complete |  |
| C-03.AC-16 | S-013 | complete |  |
| C-03.AC-17 | S-011 | complete |  |
| C-03.AC-18 | S-013 | complete |  |
| C-03.AC-19 | S-011 | complete |  |
| C-03.AC-20 | S-062 (part), S-014 | complete |  |
| C-03.AC-21 | S-062 | complete |  |
| C-03.AC-22 | S-062 (part), S-014 (part), S-016 | complete | the Personal access token part needs C-04 and is verified by S-016; the sign-in page by S-014 |
| C-03.AC-23 | S-062 | complete |  |
| C-03.AC-24 | S-062 (part), S-016 | complete | the Personal access token part needs C-04 and is verified by S-016 |
| C-03.AC-25 | S-011 | complete |  |
| C-03.AC-26 | S-013 (part), S-062 (part), S-015 | complete |  |
| C-03.AC-27 | S-063 (part), S-049 (part) | complete | needs Owners (S-032, released by S-063) and a messenger with the ack timeout (S-049), both later than the user administration of S-011 |

## C-04

[API tokens](../prd/l1/C-04-api-tokens.md) · Foundation · stories: S-016, S-017

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-04.FR-1 | S-016, S-017 | complete |  |
| C-04.FR-2 | S-016 (part), S-017 (part), S-032 (part), S-049 | complete | "Still on it" refused for Service accounts (C-17, S-049) |
| C-04.FR-3 | S-016 (part), S-017 | complete |  |
| C-04.FR-4 | S-016 (part), S-018 | complete | ingestion refuses API tokens (C-05) |
| C-04.FR-5 | S-016 | complete |  |
| C-04.FR-6 | S-016, S-017 | complete |  |
| C-04.FR-7 | S-016, S-017 | complete |  |
| C-04.FR-8 | S-016 (part), S-017, S-056 (part) | complete | editing the grace period on the Security page is S-056 (C-20.FR-1) |
| C-04.AC-1 | S-016 | complete |  |
| C-04.AC-2 | S-016, S-017 | complete |  |
| C-04.AC-3 | S-016 | complete |  |
| C-04.AC-4 | S-016 | complete |  |
| C-04.AC-5 | S-016 | complete |  |
| C-04.AC-6 | S-032 (part), S-063 (part) | complete | verified by S-032 and S-063: Acknowledge, Resolve and Snooze are the commands of S-032, Notes come with S-063 |
| C-04.AC-7 | S-016 | complete |  |
| C-04.AC-8 | S-016 | complete |  |
| C-04.AC-9 | S-016 | complete |  |

## C-05

[Integrations](../prd/l1/C-05-integrations.md) · Observation · stories: S-018, S-019

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-05.FR-1 | S-018 (part), S-019 (part), S-023 (part), S-024 | complete |  |
| C-05.FR-2 | S-018 (part), S-019 | complete |  |
| C-05.FR-3 | S-018 | complete |  |
| C-05.FR-4 | S-018 (part), S-020 | complete |  |
| C-05.FR-5 | S-018 (part), S-019 | complete |  |
| C-05.FR-6 | S-018 (part), S-020 | complete |  |
| C-05.FR-7 | S-018 (part), S-019 (part), S-020 (part), S-022 (part), S-024 | complete | the Heartbeat state joins with C-07 (S-024) |
| C-05.FR-8 | S-018 (part), S-019, S-021 (part), S-028 | complete | the effects on Alerts and Alert Groups come with C-06 and C-09 (S-021, S-028) |
| C-05.FR-10 | S-018 | complete |  |
| C-05.FR-11 | S-018 | complete |  |
| C-05.FR-12 | S-018 | complete |  |
| C-05.AC-1 | S-018 | complete |  |
| C-05.AC-2 | S-018 | complete |  |
| C-05.AC-3 | S-018 (part), S-019 | complete |  |
| C-05.AC-5 | S-018 | complete |  |
| C-05.AC-6 | S-018 (part), S-019 | complete |  |
| C-05.AC-7 | S-018 | complete |  |
| C-05.AC-8 | S-018 (part), S-020 | complete | verified by S-020: the state `failed` needs processing (C-06); S-018 checks the stored bytes |

## C-06

[Snapshot processing](../prd/l1/C-06-snapshot-processing.md) · Observation · stories: S-020, S-021, S-022, S-065

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-06.FR-1 | S-020, S-065 (part) | complete | S-065 changes the order from per Integration to per Alertmanager group |
| C-06.FR-2 | S-020 | complete |  |
| C-06.FR-3 | S-020 (part), S-028 | complete | the warning in the Timeline needs Alert Groups (C-09, S-028) |
| C-06.FR-4 | S-020 (part), S-028 | complete | resolving in the latest Alert Group needs Alert Groups (C-09, S-028) |
| C-06.FR-21 | S-020 | complete |  |
| C-06.FR-5 | S-020 | complete |  |
| C-06.FR-6 | S-020 (part), S-021, S-023 | complete | the time-based end of truncation needs the liveness clock of C-07 (S-023) |
| C-06.FR-7 | S-020 (part), S-023 | complete | Stale in each `groupKey` needs the Heartbeat of C-07 (S-023) |
| C-06.FR-8 | S-020 (part), S-023 | complete | the Stale scan needs the Heartbeat of C-07 (S-023) |
| C-06.FR-9 | S-023 | complete | the Heartbeat is C-07 (S-023) |
| C-06.FR-10 | S-020 (part), S-023 | complete | the Stale reason needs the Heartbeat of C-07 (S-023) |
| C-06.FR-11 | S-020 | complete |  |
| C-06.FR-12 | S-020 (part), S-028 (part) | complete | the Root message update arrives with C-11 |
| C-06.FR-13 | S-020 | complete |  |
| C-06.FR-14 | S-021, S-022 | complete |  |
| C-06.FR-15 | S-020 (part), S-021 (part), S-023 | complete | the reason `stale` needs the Heartbeat of C-07 (S-023) |
| C-06.FR-16 | S-021 (part), S-023 | complete | `MusterHeartbeatLost` is C-07 (S-023) |
| C-06.FR-17 | S-021 | complete |  |
| C-06.FR-18 | S-021 (part), S-022 | complete |  |
| C-06.FR-19 | S-020 (part), S-021 (part), S-022 (part), S-025 (part), S-027 (part), S-028 (part), S-031 | complete | Route, Severity level and Alert Group join with C-08 and C-09 (S-025, S-027, S-028, S-031) |
| C-06.FR-20 | S-020 (part), S-021 | complete |  |
| C-06.AC-1 | S-020 | complete |  |
| C-06.AC-2 | S-020 | complete |  |
| C-06.AC-3 | S-021 (part), S-022 (part), S-023 | complete | verified by S-023: "never Stale without a Heartbeat" needs the Stale scan, which arrives with the Heartbeat (C-07); S-021 and S-022 check the learned interval |
| C-06.AC-4 | S-020 | complete |  |
| C-06.AC-5 | S-021 | complete |  |
| C-06.AC-6 | S-021 | complete |  |
| C-06.AC-7 | S-021 | complete |  |
| C-06.AC-8 | S-020 (part), S-022 | complete |  |
| C-06.AC-9 | S-020 | complete |  |
| C-06.AC-10 | S-020 (part), S-021 | complete |  |
| C-06.AC-11 | S-021 | complete |  |
| C-06.AC-12 | S-020 | complete |  |
| C-06.AC-13 | S-020 | complete |  |
| C-06.AC-14 | S-020 | complete |  |
| C-06.AC-15 | S-020 | complete |  |

## C-07

[Heartbeat](../prd/l1/C-07-heartbeat.md) · Observation · stories: S-023, S-024

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-07.FR-1 | S-023 | complete |  |
| C-07.FR-2 | S-023 (part), S-024 | complete |  |
| C-07.FR-3 | S-023 (part), S-024 | complete |  |
| C-07.FR-4 | S-023 | complete |  |
| C-07.FR-5 | S-023 (part), S-024 | complete |  |
| C-07.FR-6 | S-023 (part), S-024 | complete |  |
| C-07.FR-8 | S-023 | complete |  |
| C-07.AC-1 | S-023 | complete |  |
| C-07.AC-2 | S-023 | complete |  |
| C-07.AC-3 | S-023 | complete |  |
| C-07.AC-4 | S-023 | complete |  |
| C-07.AC-5 | S-023 (part), S-024 | complete |  |

## C-08

[Routing](../prd/l1/C-08-routing.md) · Observation · stories: S-025, S-026, S-027

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-08.FR-1 | S-025 (part), S-027 (part), S-031 (part), S-033 (part), S-034 (part), S-035 (part), S-036 (part), S-038 (part), S-064 (part), S-050 (part) | complete | later policy sections and the Destinations picker come with their capabilities (S-031, S-033, S-038, S-064, S-050); S-035 writes the Destinations of a Route |
| C-08.FR-2 | S-025 (part), S-027 | complete |  |
| C-08.FR-3 | S-025 (part), S-027 | complete |  |
| C-08.FR-4 | S-025 (part), S-028 | complete | grouping by the key needs Alert Groups (C-09, S-028) |
| C-08.FR-5 | S-026 (part), S-027 | complete |  |
| C-08.FR-6 | S-025 (part), S-028 | complete | the Alert Group's level and urgency need Alert Groups (C-09, S-028) |
| C-08.FR-7 | S-025 (part), S-027 | complete |  |
| C-08.FR-8 | S-025 (part), S-028 | complete | open Alert Groups need C-09 (S-028) |
| C-08.FR-9 | S-025 (part), S-028 | complete | Routes with open Alert Groups need C-09 (S-028, S-031) |
| C-08.FR-10 | S-025 | complete |  |
| C-08.FR-11 | S-026 (part), S-027, S-061 (part), S-064 (part) | complete | the `internal_alerts` suggestion is C-13.FR-11 (S-061, S-064) |
| C-08.FR-12 | S-025 | complete |  |
| C-08.FR-13 | S-025 (part), S-027 | complete |  |
| C-08.AC-1 | S-025 | complete |  |
| C-08.AC-2 | S-026 | complete |  |
| C-08.AC-6 | S-025 | complete |  |
| C-08.AC-7 | S-025 | complete |  |
| C-08.AC-8 | S-026 (part), S-027 | complete |  |
| C-08.AC-9 | S-025 (part), S-027 | complete |  |
| C-08.AC-10 | S-025 | complete |  |
| C-08.AC-11 | S-025 (part), S-026 | complete |  |
| C-08.AC-12 | S-026 | complete |  |

## C-09

[Alert Group lifecycle](../prd/l1/C-09-alert-group-lifecycle.md) · Observation · stories: S-028, S-029, S-030, S-031, S-065

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-09.FR-1 | S-028 (part), S-032, S-049 (part) | complete | Unclaimed is added by C-17 (S-049) |
| C-09.FR-2 | S-028 | complete |  |
| C-09.FR-23 | S-028 (part), S-029 | complete |  |
| C-09.FR-3 | S-028, S-065 (part) | complete | S-065 takes the counter row only for creations and reopens |
| C-09.FR-4 | S-028 (part), S-031 (part), S-032 | complete | the acknowledged and snoozed cases need Acknowledge and Snooze (C-10, S-032) |
| C-09.FR-5 | S-028 (part), S-031 (part), S-032 | complete | the Grace period follows a person's Resolve (C-10, S-032) |
| C-09.FR-6 | S-028 (part), S-032 | complete | the acknowledged and snoozed cases need Acknowledge and Snooze (C-10, S-032) |
| C-09.FR-7 | S-028 (part), S-030 | complete |  |
| C-09.FR-8 | S-063 (part), S-049 | complete | the Snooze is C-10 (S-032, its end S-063); the ack timeout starting over is C-17 (S-049) |
| C-09.FR-9 | S-028 (part), S-031 (part), S-032 | complete | the Loud cases need Acknowledge and Snooze (C-10, S-032) |
| C-09.FR-10 | S-028 (part), S-030 (part), S-036 (part) | complete | from C-11 on also in messages |
| C-09.FR-11 | S-028 (part), S-030 (part), S-032 (part), S-063 (part), S-034 (part), S-049 (part) | complete | each later capability adds its entry types (S-032, S-063, S-034, S-049) |
| C-09.FR-12 | S-028 (part), S-063 | complete | Snooze ends come with S-063 |
| C-09.FR-18 | S-028 | complete |  |
| C-09.FR-19 | S-028 (part), S-031, S-035 (part) | complete |  |
| C-09.FR-22 | S-028 (part), S-063 (part) | complete |  |
| C-09.FR-13 | S-029 (part), S-030, S-063 (part), S-033, S-061 (part), S-064 (part), S-049 (part), S-050 (part) | complete | later filters come with S-063, S-064 and S-050 |
| C-09.FR-17 | S-030 | complete |  |
| C-09.FR-24 | S-030 (part), S-033 | complete |  |
| C-09.FR-25 | S-012 (part), S-029 (part), S-030 | complete | S-012 brought the stream forward with the notice and Organization hints |
| C-09.FR-14 | S-028 (part), S-030, S-033, S-034 (part), S-037 (part), S-038 (part), S-064 (part), S-050 (part) | complete | later sections come with S-038, S-064 and S-050 |
| C-09.FR-20 | S-029 (part), S-030 | complete |  |
| C-09.FR-15 | S-029 (part), S-031 | complete |  |
| C-09.FR-16 | S-029 (part), S-030 (part), S-063 | complete | Notes are C-10 (S-063) |
| C-09.FR-21 | S-029 (part), S-031 | complete |  |
| C-09.AC-1 | S-028 | complete |  |
| C-09.AC-2 | S-028 | complete |  |
| C-09.AC-4 | S-028 (part), S-030 | complete |  |
| C-09.AC-5 | S-029 | complete |  |
| C-09.AC-6 | S-028 | complete |  |
| C-09.AC-7 | S-028 | complete |  |
| C-09.AC-8 | S-028 | complete |  |
| C-09.AC-9 | S-028 | complete |  |
| C-09.AC-10 | S-028 | complete |  |
| C-09.AC-11 | S-028 | complete |  |
| C-09.AC-12 | S-028 | complete |  |
| C-09.AC-13 | S-029 (part), S-030 | complete |  |
| C-09.AC-14 | S-028 | complete |  |
| C-09.AC-15 | S-029 (part), S-030 | complete |  |
| C-09.AC-16 | S-029 (part), S-030 | complete |  |
| C-09.AC-17 | S-029 (part), S-031 | complete |  |
| C-09.AC-18 | S-029 (part), S-030 | complete |  |
| C-09.AC-19 | S-030 | complete |  |
| C-09.AC-20 | S-028 (part), S-029 | complete |  |
| C-09.AC-21 | S-028 | complete |  |
| C-09.AC-22 | S-028 | complete |  |
| C-09.AC-23 | S-029 | complete |  |
| C-09.AC-24 | S-012, S-029 | complete | verified by S-012, which brought the stream forward with C-03, and again by S-029 |
| C-09.AC-25 | S-029 | complete |  |

## C-10

[Commands](../prd/l1/C-10-commands.md) · Observation · stories: S-032, S-063, S-033

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-10.FR-1 | S-032 (part), S-063 (part), S-033 | complete |  |
| C-10.FR-2 | S-032 (part), S-033 | complete |  |
| C-10.FR-3 | S-032, S-061 (part), S-042 (part) | complete | the messenger Transports are passed in by S-061 and S-042 |
| C-10.FR-4 | S-032 (part), S-033, S-049 (part) | complete | Reminders starting over for the new Owner are C-17 (S-049) |
| C-10.FR-5 | S-032 | complete |  |
| C-10.FR-6 | S-032 (part), S-063 (part), S-033, S-061 (part), S-042 (part) | complete | the messenger durations come with S-061 and S-042 |
| C-10.FR-7 | S-032 (part), S-033 | complete |  |
| C-10.FR-8 | S-063 (part), S-033 | complete |  |
| C-10.FR-10 | S-032 | complete |  |
| C-10.FR-11 | S-032 (part), S-061 (part), S-042 (part), S-051 | complete | the messenger adapters are S-061 and S-042; refusals of presses without an Account link are verified with real links by S-051 |
| C-10.FR-12 | S-032 (part), S-063 (part) | complete |  |
| C-10.FR-13 | S-063 (part), S-033 | complete |  |
| C-10.FR-14 | S-032 (part), S-033 | complete |  |
| C-10.FR-15 | S-032 (part), S-063 (part) | complete |  |
| C-10.FR-16 | S-032 (part), S-063 (part), S-033, S-049 (part), S-050 (part) | complete | `still_on_it` is C-17 (S-049, S-050) |
| C-10.AC-1 | S-032 | complete |  |
| C-10.AC-2 | S-032 | complete |  |
| C-10.AC-3 | S-032 | complete |  |
| C-10.AC-4 | S-032 | complete |  |
| C-10.AC-5 | S-032 | complete |  |
| C-10.AC-6 | S-032 | complete |  |
| C-10.AC-7 | S-032 | complete |  |
| C-10.AC-8 | S-032 | complete |  |
| C-10.AC-9 | S-032 | complete |  |
| C-10.AC-10 | S-063 | complete |  |
| C-10.AC-11 | S-032 | complete |  |
| C-10.AC-12 | S-032 | complete |  |
| C-10.AC-13 | S-032 | complete |  |
| C-10.AC-14 | S-032 (part), S-033 | complete |  |
| C-10.AC-15 | S-032 | complete |  |
| C-10.AC-16 | S-032 (part), S-063 (part) | complete |  |
| C-10.AC-17 | S-032 | complete |  |
| C-10.AC-18 | S-032 (part), S-063 (part) | complete |  |
| C-10.AC-19 | S-063 | complete |  |
| C-10.AC-20 | S-063 | complete |  |

## C-11

[Delivery engine](../prd/l1/C-11-delivery-engine.md) · Shadow · stories: S-034, S-035

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-11.FR-1 | S-034 | complete |  |
| C-11.FR-2 | S-034 (part), S-039 (part), S-061 (part), S-047 (part) | complete | its callers: checks from S-039, ephemeral answers from S-061, `limited` test steps from S-047 |
| C-11.FR-3 | S-034 | complete |  |
| C-11.FR-4 | S-034 | complete |  |
| C-11.FR-5 | S-034 | complete |  |
| C-11.FR-6 | S-035 (part), S-044 (part), S-045 (part) | complete |  |
| C-11.FR-7 | S-034 (part), S-061 (part), S-042 (part) | complete |  |
| C-11.FR-8 | S-034 (part), S-035 (part), S-061 (part), S-042 (part), S-044 (part) | complete |  |
| C-11.FR-9 | S-035 (part), S-061 (part), S-064 (part), S-042 (part), S-044 (part), S-047 (part) | complete |  |
| C-11.FR-10 | S-035, S-064 (part) | complete |  |
| C-11.FR-11 | S-035 | complete |  |
| C-11.FR-12 | S-035 | complete |  |
| C-11.FR-13 | S-035 | complete |  |
| C-11.FR-14 | S-035 (part), S-039 (part), S-044 (part), S-045 (part) | complete |  |
| C-11.FR-15 | S-034 | complete |  |
| C-11.FR-16 | S-034 (part), S-035 (part), S-064 (part), S-042 (part) | complete |  |
| C-11.FR-17 | S-034 (part), S-035 (part) | complete |  |
| C-11.FR-18 | S-034 (part), S-035 (part), S-039 (part), S-042 (part), S-044 (part) | complete |  |
| C-11.FR-19 | S-035 (part), S-044 (part) | complete |  |
| C-11.FR-20 | S-034 (part), S-035 (part), S-049 (part) | complete | delivery handles the C-17 rows from S-034 on; they are produced from S-049 on |
| C-11.FR-21 | S-034 (part), S-035 (part), S-042 (part) | complete |  |
| C-11.AC-1 | S-034, S-061 (part) | complete | through the recording test adapter; repeated against the fake Mattermost server in S-061 |
| C-11.AC-2 | S-034 | complete | through the recording test adapter; repeated against the fake Mattermost server in S-061 (C-13.AC-15) |
| C-11.AC-3 | S-035 | complete | through the recording test adapter; repeated against the fake Mattermost server in S-061 |
| C-11.AC-4 | S-035 | complete | through the recording test adapter; repeated in S-061 (C-13.AC-12) |
| C-11.AC-5 | S-035, S-061 (part) | complete | through the recording test adapter; repeated in S-061 with a restart of Muster |
| C-11.AC-6 | S-035 | complete | through the recording test adapter; repeated in S-061 |
| C-11.AC-7 | S-035, S-061 (part) | complete | through the recording test adapter; repeated in S-061 |
| C-11.AC-8 | S-035 | complete | through the recording test adapter; repeated in S-061 (C-13.AC-10) |
| C-11.AC-9 | S-035 | complete |  |
| C-11.AC-10 | S-034 (part), S-035 (part) | complete |  |
| C-11.AC-11 | S-035 | complete | through the recording test adapter; repeated in S-061 (C-13.AC-8) and S-042 (C-14.AC-12) |
| C-11.AC-12 | S-035 | complete |  |
| C-11.AC-13 | S-035 | complete |  |
| C-11.AC-14 | S-034 | complete |  |

## C-12

[Messages](../prd/l1/C-12-messages.md) · Shadow · stories: S-036, S-037, S-038

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-12.FR-1 | S-036 (part), S-037 (part), S-061 (part), S-042 (part) | complete |  |
| C-12.FR-2 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-3 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-4 | S-036 (part), S-037 (part), S-045 (part), S-049 (part) | complete | the ack timeout notice is rendered from S-049 on |
| C-12.FR-5 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-6 | S-036 (part), S-037 (part), S-038 (part) | complete |  |
| C-12.FR-7 | S-036 (part), S-037 (part), S-061 (part), S-042 (part) | complete |  |
| C-12.FR-8 | S-037 (part), S-039 (part), S-061 (part), S-064 (part), S-042 (part), S-044 (part), S-045 (part) | complete |  |
| C-12.FR-9 | S-037 (part), S-038 (part) | complete |  |
| C-12.FR-10 | S-036 | complete |  |
| C-12.FR-11 | S-036 (part), S-061 (part), S-042 (part) | complete |  |
| C-12.FR-12 | S-037, S-051 (part) | complete | checked with real Account links in C-18.AC-7 (S-051) |
| C-12.FR-13 | S-036 (part), S-037 (part), S-045 (part) | complete |  |
| C-12.AC-1 | S-036 (part), S-037 (part) | complete |  |
| C-12.AC-2 | S-036 (part), S-038 (part) | complete |  |
| C-12.AC-3 | S-036, S-061 (part) | complete | through the recording test adapter; repeated against the fake Mattermost server in S-061 |
| C-12.AC-4 | S-036 | complete |  |
| C-12.AC-5 | S-036 | complete | the default message through previews; Thread replies through the recording test adapter and in S-061 |
| C-12.AC-6 | S-037 (part), S-038 (part) | complete |  |

## C-13

[Mattermost](../prd/l1/C-13-mattermost.md) · Shadow · stories: S-039, S-061, S-040, S-064

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-13.FR-1 | S-039 (part), S-040 (part) | complete |  |
| C-13.FR-2 | S-039 (part), S-040 (part), S-061 (part), S-064 (part) | complete | the warning `press_answers_in_thread` in S-061, shown on the Connection page in S-040 |
| C-13.FR-3 | S-039 (part), S-061 (part) | complete |  |
| C-13.FR-4 | S-061 (part), S-049 (part) | complete | presses from linked accounts are tested with link rows set up directly; Account links are created from S-051; presses on Thread replies come with the Reminders of S-049 |
| C-13.FR-5 | S-061 | complete |  |
| C-13.FR-6 | S-039 | complete |  |
| C-13.FR-7 | S-061 | complete |  |
| C-13.FR-8 | S-061 | complete |  |
| C-13.FR-9 | S-064 | complete |  |
| C-13.FR-10 | S-039 (part), S-061 (part), S-064 (part) | complete |  |
| C-13.FR-11 | S-061 (part), S-064 (part) | complete |  |
| C-13.FR-12 | S-061 (part), S-064 (part) | complete |  |
| C-13.FR-13 | S-039 (part), S-040 (part), S-047 (part), S-048 (part) | complete |  |
| C-13.AC-1 | S-061 | complete |  |
| C-13.AC-2 | S-061 | complete |  |
| C-13.AC-3 | S-061 (part), S-064 (part) | complete |  |
| C-13.AC-4 | S-061 | complete |  |
| C-13.AC-5 | S-061 | complete |  |
| C-13.AC-6 | S-061 | complete |  |
| C-13.AC-7 | S-039 | complete |  |
| C-13.AC-8 | S-061 (part), S-064 (part) | complete |  |
| C-13.AC-9 | S-061 (part), S-064 (part) | complete |  |
| C-13.AC-10 | S-061 (part), S-064 (part) | complete |  |
| C-13.AC-11 | S-061 | complete |  |
| C-13.AC-12 | S-061 | complete |  |
| C-13.AC-13 | S-061 | complete |  |
| C-13.AC-14 | S-061 | complete |  |
| C-13.AC-15 | S-061 | complete |  |
| C-13.AC-16 | S-061 (part), S-040 (part) | complete | the warning in the API and `muster doctor` in S-061; on the Connection page in S-040 |

## C-14

[Telegram](../prd/l1/C-14-telegram.md) · Shadow · stories: S-041, S-042, S-043

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-14.FR-1 | S-041 (part), S-043 (part) | complete |  |
| C-14.FR-2 | S-042 (part), S-043 (part) | complete |  |
| C-14.FR-3 | S-042 | complete |  |
| C-14.FR-4 | S-042, S-049 (part) | complete | presses from linked accounts are tested with link rows set up directly; Account links are created from S-051; presses on Thread replies come with the Reminders of S-049 |
| C-14.FR-5 | S-042 | complete | the answer to a linked account is checked with link rows set up directly (S-051 creates links) |
| C-14.FR-6 | S-042 | complete |  |
| C-14.FR-7 | S-042 | complete |  |
| C-14.FR-8 | S-041 (part), S-051 | complete | the router receives `/start`; the bot answers it once Account links exist (S-051) |
| C-14.FR-9 | S-042 (part), S-058 | complete | the section on restricted networks is C-21.FR-7 (S-058) |
| C-14.FR-10 | S-041 (part), S-043 (part) | complete |  |
| C-14.FR-11 | S-041 (part), S-043 (part) | complete |  |
| C-14.FR-12 | S-041 (part), S-042 (part) | complete |  |
| C-14.FR-13 | S-042 | complete |  |
| C-14.FR-14 | S-042 (part), S-043 (part) | complete |  |
| C-14.FR-15 | S-042 | complete |  |
| C-14.FR-16 | S-042 | complete |  |
| C-14.AC-1 | S-042 | complete |  |
| C-14.AC-2 | S-042 | complete |  |
| C-14.AC-3 | S-041 | complete |  |
| C-14.AC-4 | S-042 | complete | by the press-age rule of C-14.FR-4 |
| C-14.AC-5 | S-041 | complete |  |
| C-14.AC-6 | S-041 (part), S-043 (part) | complete |  |
| C-14.AC-7 | S-041 (part), S-042 (part), S-043 (part) | complete |  |
| C-14.AC-8 | S-042 | complete |  |
| C-14.AC-9 | S-042 | complete |  |
| C-14.AC-10 | S-041 | complete |  |
| C-14.AC-11 | S-041 (part), S-042 (part) | complete |  |
| C-14.AC-12 | S-042 | complete |  |
| C-14.AC-13 | S-042 (part), S-043 (part) | complete |  |
| C-14.AC-14 | S-042 | complete |  |
| C-14.AC-15 | S-042 | complete |  |
| C-14.AC-16 | S-042 (part), S-043 (part) | complete |  |
| C-14.AC-17 | S-042 | complete |  |
| C-14.AC-18 | S-042 | complete |  |
| C-14.AC-19 | S-042 | complete |  |

## C-15

[Outgoing webhook](../prd/l1/C-15-outgoing-webhook.md) · Shadow · stories: S-044, S-045, S-046

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-15.FR-1 | S-044 (part), S-045 (part), S-046 (part) | complete |  |
| C-15.FR-2 | S-044 (part), S-047 (part) | complete |  |
| C-15.FR-3 | S-045, S-046 (part) | complete |  |
| C-15.FR-4 | S-045 | complete |  |
| C-15.FR-5 | S-044 (part), S-046 (part) | complete |  |
| C-15.FR-6 | S-044 | complete |  |
| C-15.FR-7 | S-045 | complete |  |
| C-15.FR-8 | S-044 | complete |  |
| C-15.FR-9 | S-044 (part), S-045 (part) | complete |  |
| C-15.FR-10 | S-044 (part), S-046 (part), S-047 (part) | complete |  |
| C-15.FR-11 | S-044 (part), S-045 (part) | complete |  |
| C-15.FR-12 | S-044 (part), S-045 (part), S-046 (part) | complete |  |
| C-15.AC-1 | S-044 | complete |  |
| C-15.AC-2 | S-044 | complete |  |
| C-15.AC-3 | S-045 | complete |  |
| C-15.AC-4 | S-044 | complete |  |
| C-15.AC-5 | S-044 | complete |  |
| C-15.AC-6 | S-044 | complete |  |
| C-15.AC-7 | S-044 | complete |  |
| C-15.AC-8 | S-044 | complete |  |
| C-15.AC-9 | S-044 (part), S-045 (part) | complete |  |
| C-15.AC-10 | S-044 (part), S-045 (part) | complete |  |
| C-15.AC-11 | S-044 (part), S-049 (part) | complete | the timer rows come from the running timers with S-049 |
| C-15.AC-12 | S-044 | complete |  |
| C-15.AC-13 | S-044 | complete |  |

## C-16

[Destination test](../prd/l1/C-16-destination-test.md) · Shadow · stories: S-047, S-048

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-16.FR-1 | S-047 (part), S-048 (part) | complete |  |
| C-16.FR-2 | S-047 (part), S-048 (part) | complete |  |
| C-16.FR-3 | S-047 (part), S-048 (part) | complete |  |
| C-16.FR-4 | S-047 (part), S-048 (part) | complete |  |
| C-16.FR-5 | S-047 | complete |  |
| C-16.FR-6 | S-047 | complete | a scope rule: a test is one "create" request, nothing more |
| C-16.FR-7 | S-047 | complete |  |
| C-16.AC-1 | S-047 | complete |  |
| C-16.AC-2 | S-047 | complete |  |
| C-16.AC-3 | S-047 (part), S-048 (part) | complete |  |
| C-16.AC-4 | S-047 | complete |  |
| C-16.AC-5 | S-047 | complete |  |
| C-16.AC-6 | S-047 | complete |  |
| C-16.AC-7 | S-047 (part), S-048 (part) | complete |  |

## C-17

[Timers](../prd/l1/C-17-timers.md) · Actions · stories: S-049, S-050

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-17.FR-1 | S-049 | complete |  |
| C-17.FR-2 | S-049 (part), S-050 (part) | complete |  |
| C-17.FR-3 | S-049 (part), S-050 (part) | complete |  |
| C-17.FR-4 | S-049 | complete |  |
| C-17.FR-5 | S-049 | complete |  |
| C-17.FR-6 | S-049, S-055 (part) | complete | S-055's end-to-end test repeats the expiry after a real key rotation |
| C-17.FR-7 | S-049 | complete |  |
| C-17.FR-8 | S-049 | complete | the release itself is S-063 (C-03.FR-13) |
| C-17.FR-9 | S-049 (part), S-050 (part) | complete |  |
| C-17.FR-10 | S-049 (part), S-050 (part), S-051 (part) | complete | presses from links made through the profile with S-051 |
| C-17.FR-11 | S-049 | complete |  |
| C-17.AC-1 | S-049 | complete |  |
| C-17.AC-2 | S-049 | complete |  |
| C-17.AC-3 | S-049 | complete |  |
| C-17.AC-4 | S-049 | complete |  |
| C-17.AC-5 | S-049 | complete |  |
| C-17.AC-6 | S-049 (part), S-050 (part) | complete |  |
| C-17.AC-7 | S-049 | complete |  |
| C-17.AC-8 | S-049 (part), S-050 (part) | complete |  |
| C-17.AC-9 | S-049 | complete |  |

## C-18

[Account links](../prd/l1/C-18-account-links.md) · Actions · stories: S-051, S-052

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-18.FR-1 | S-051 | complete |  |
| C-18.FR-2 | S-051 (part), S-052 (part) | complete |  |
| C-18.FR-3 | S-051 (part), S-052 (part) | complete |  |
| C-18.FR-4 | S-051 | complete |  |
| C-18.FR-5 | S-051 | complete |  |
| C-18.FR-6 | S-051 (part), S-052 (part) | complete |  |
| C-18.FR-7 | S-051 | complete |  |
| C-18.FR-8 | S-051 | complete |  |
| C-18.FR-9 | S-051 | complete |  |
| C-18.FR-10 | S-051 (part), S-052 (part) | complete |  |
| C-18.FR-11 | S-051 | complete |  |
| C-18.AC-1 | S-051 | complete |  |
| C-18.AC-2 | S-051 | complete |  |
| C-18.AC-3 | S-051 | complete |  |
| C-18.AC-4 | S-051 | complete |  |
| C-18.AC-5 | S-051 | complete | the Reminder buttons are C-17 (S-049) |
| C-18.AC-6 | S-051 | complete |  |
| C-18.AC-7 | S-051 | complete |  |

## C-19

[Self-observation](../prd/l1/C-19-self-observation.md) · Operations · stories: S-053, S-054, S-059

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-19.FR-2 | S-053 | complete |  |
| C-19.FR-3 | S-059 (part), S-060 (part) | complete | the rules are in the chart with S-059; the bypass route is documented by S-060 |
| C-19.FR-4 | S-059 | complete |  |
| C-19.FR-5 | S-053 | complete |  |
| C-19.FR-7 | S-053 (part), S-054 (part) | complete |  |
| C-19.FR-9 | S-053 (part), S-058 (part), S-059 | complete | Internal alerts S-053, pages S-058, chart rules S-059 |
| C-19.FR-10 | S-053 (part), S-054 (part) | complete |  |
| C-19.AC-1 | S-053 (part), S-059 | complete | the outgoing heartbeat stops without a Leader (S-053); `MusterNoLeader` is the chart rule of S-059, checked by its unit test |
| C-19.AC-2 | S-053 | complete |  |
| C-19.AC-3 | S-053 | complete |  |
| C-19.AC-4 | S-058 (part), S-059 | complete | verified by S-059: the rules arrive in the chart after their pages (S-058) |
| C-19.AC-5 | S-053 | complete |  |
| C-19.AC-6 | S-053 (part), S-054 (part) | complete |  |
| C-19.AC-7 | S-053 | complete |  |
| C-19.AC-8 | S-059 | complete |  |

## C-20

[Organization settings and security](../prd/l1/C-20-organization-settings-and-security.md) · Operations · stories: S-055, S-056

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-20.FR-1 | S-055 (part), S-056 (part) | complete |  |
| C-20.FR-2 | S-055 | complete |  |
| C-20.FR-3 | S-055 (part), S-056 (part) | complete |  |
| C-20.FR-6 | S-055 (part), S-056 (part) | complete |  |
| C-20.FR-7 | S-055 | complete |  |
| C-20.FR-8 | S-055 | complete |  |
| C-20.AC-1 | S-055 | complete |  |
| C-20.AC-2 | S-055 (part), S-056 (part) | complete |  |
| C-20.AC-3 | S-055 | complete |  |
| C-20.AC-4 | S-055 (part), S-056 (part) | complete |  |
| C-20.AC-5 | S-055 | complete |  |
| C-20.AC-6 | S-055 | complete |  |

## C-21

[Documentation site and runbooks](../prd/l1/C-21-documentation-site-and-runbooks.md) · Operations · stories: S-057, S-058, S-060

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-21.FR-1 | S-057 | complete |  |
| C-21.FR-2 | S-057 (part), S-058 (part), S-060 (part) | complete |  |
| C-21.FR-3 | S-058 (part), S-059 | complete | rendered chart rules join the one-to-one check with S-059 |
| C-21.FR-4 | S-057 | complete |  |
| C-21.FR-5 | S-057 (part), S-058 (part), S-060 (part) | complete |  |
| C-21.FR-7 | S-058 | complete |  |
| C-21.AC-1 | S-057 (part), S-058 (part), S-059 | complete | verified by S-059: the chart's `runbook_url`s exist once the chart renders its rules |
| C-21.AC-2 | S-060 | complete |  |
| C-21.AC-3 | S-058 | complete |  |

## Non-functional requirements

The [non-functional requirements](../prd/L1.md#3-non-functional-requirements) have no FR or AC IDs; each is owned by
the stories that implement and check its parts. A story that proves an NFR says so in its acceptance or contracts.

| NFR | Owned by | What each story contributes |
|---|---|---|
| NFR-1 Throughput and scale | S-004, S-018, S-061, S-065 | the load-test harness and its nightly job (S-004); accepting webhooks and storing Snapshots at the burst rate (S-018); the full profile — 50 webhooks per second for a minute, 10,000 Alerts in 1,000 open Alert Groups — that fails the nightly job on a breach (S-061); processing the burst of one Integration without a backlog, Alertmanager groups in parallel (S-065) |
| NFR-2 Latency | S-018, S-034, S-059, S-061, S-065 | `muster_ingest_request_duration_seconds` (S-018); `muster_delivery_latency_seconds` (S-034); the chart rule `MusterDeliverySlow` (S-059); the thresholds of P-44 and of the 95th percentile in the load test (S-061); the delivery latency under the full profile (S-065) |
| NFR-3 Footprint | S-003, S-004 | the compose example with PostgreSQL's memory capped (S-003); the nightly measurement of the peak working sets (S-004) |
| NFR-4 Availability | S-004, S-006, S-008, S-018, S-020, S-028, S-034, S-035 | the nightly two-replica end-to-end run with `muster dev --replica` (S-004); readiness on the database only (S-006); the Leader replaced within a minute and the recovery notice (S-008); `202` only after the Snapshot is stored (S-018); ingestion, timers and delivery on every replica (S-020, S-028, S-034); Broken Destinations that recover to the current state (S-035) |
| NFR-5 Durability and backup | S-008, S-060 | `muster doctor` with the key canary check (S-008); the backup and restore page with the restore order (S-060) |
| NFR-6 Security | S-001, S-002, S-003, S-007, S-009, S-010, S-012, S-013, S-062, S-016, S-018, S-036, S-041, S-044, S-055 | the secret-leak lint (S-001); dependency scanning (S-002); signed images and the SBOM (S-003); AES-256-GCM under the Keyring (S-007) and its rotation (S-055); the outbound address policy, no redirects, per-client proxies (S-009); secure cookies, CSRF, the Content Security Policy, argon2id and sign-in throttling (S-010); TOTP (S-012); write-only secret fields and OIDC refusing users without a mapped group (S-013); OIDC re-checks at the IdP (S-062); hashed tokens (S-016, S-018); escaping, neutralized Mentions and signed buttons (S-036); the Telegram token sent only to the saved Bot API address (S-041); outgoing webhook Secrets (S-044) |
| NFR-7 Privacy | S-005, S-011 | metric labels without alert labels, users or Alert Group numbers (S-005); pseudonymized deletion (S-011); no telemetry and no email are properties of the whole design, kept by review |
| NFR-8 Internationalization | S-014, S-036, S-057 | the UI in English and Russian with plural forms and a lint for missing keys (S-014); built-in message texts in both languages, chosen per Route (S-036); the documentation in English (S-057) |
| NFR-9 Time | S-008, S-010, S-014, S-036, S-060 | the clock skew check (S-008); UTC in the API (S-010); the user's time zone in the UI (S-014); absolute times in the Organization's time zone in messages (S-036); NTP on the nodes in the high-availability page (S-060) |
| NFR-10 Retention | S-008, S-029, S-055 | partitions dropped by retention (S-008); batched deletes of summary rows and details (S-029, with the other owners of unpartitioned tables); editing the periods (S-055) |
| NFR-11 Upgrades | S-003, S-006, S-060 | semantic versions through release-please (S-003); expand/contract migrations and the refusal of a newer schema (S-006); the upgrade page (S-060) |
| NFR-12 Compatibility | S-003, S-006, S-020, S-039, S-041 | binaries for linux and darwin on amd64 and arm64, the chart with Ingress and Gateway API routes (S-003); PostgreSQL 14 or newer and the session-capable pooler check (S-006); `notification_reason` with the fallback to identical Snapshots (S-020); Mattermost REST API v4 with a bot account (S-039); the Bot API at a configurable base URL (S-041) |
| NFR-13 API | S-002, S-010 | spec-first generation (S-002); cursor pagination, RFC 9457 errors with pointers, optimistic locking, UTC and `public_id` in URLs (S-010) |
| NFR-14 Quality | S-001, S-002, S-004, S-014 | the coverage gate (S-001); the nightly mutation report (S-002); end-to-end tests with fake servers (S-004) and a real browser (S-014); evidence for every acceptance statement in each pull request (section 7 of the [README](README.md#7-lifecycle)) |
| NFR-15 Phone width | S-030 | the Alert Group list and page at 360 CSS pixels, checked end to end at that width |
