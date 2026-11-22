# Coverage of L1 and L2 requirements by stories

- Status: Draft
- Date: 2026-10-26

Which story covers each functional requirement (`C-NN.FR-n`) and acceptance statement (`C-NN.AC-n`) of
[L1](../prd/L1.md), of the core of [L2](../prd/L2.md) and of the capabilities of L2 specified after it, from C-33
(C-34, C-36, C-37, C-38, C-39, C-40, C-41 and C-42 of the 0.2.x patch releases among them). A capability is done when
its stories together cover every ID of its file ([conventions](README.md#3-sizing-and-slicing)). The table of each
story file says whether it covers an ID fully or in part; this page joins them.

- **Covered by** lists the written stories that cover the ID, with `(part)` where a story covers only part of it.
- **Status**: `complete` — the listed stories cover the ID together; `open: S-NNN` — a planned story completes it.
  Every ID of L1, of the core of L2, of C-33, of C-34, of C-36, of C-37, of C-38, of C-39, of C-40, of C-41 and of C-42
  is complete. The sections of L2 follow the non-functional requirements of L1, and the requirements of L1 that L2
  revises have a section of their own at the end.
  [L2 §7](../prd/L2.md#7-retired-and-moved-ids) retires and moves no ID, so every ID of the capability files of L2 that
  have stories has a row here; C-43, which has none yet, gets its section with its stories.
- **Note** names, for an acceptance statement that cannot be checked when its capability merges, the later story that
  verifies it — the one in which its dependency appears — and why ([L1 conventions](../prd/L1.md#2-conventions)).

Contents: [C-01](#c-01) · [C-02](#c-02) · [C-03](#c-03) · [C-04](#c-04) · [C-05](#c-05) · [C-06](#c-06) · [C-07](#c-07)
· [C-08](#c-08) · [C-09](#c-09) · [C-10](#c-10) · [C-11](#c-11) · [C-12](#c-12) · [C-13](#c-13) · [C-14](#c-14) ·
[C-15](#c-15) · [C-16](#c-16) · [C-17](#c-17) · [C-18](#c-18) · [C-19](#c-19) · [C-20](#c-20) · [C-21](#c-21) ·
[C-22](#c-22) · [C-23](#c-23) · [C-24](#c-24) · [NFR](#non-functional-requirements) · [C-25](#c-25) · [C-26](#c-26) ·
[C-27](#c-27) · [C-28](#c-28) · [C-29](#c-29) · [C-30](#c-30) · [C-31](#c-31) · [C-32](#c-32) · [C-33](#c-33) ·
[C-34](#c-34) · [C-36](#c-36) · [C-37](#c-37) · [C-38](#c-38) · [C-39](#c-39) · [C-40](#c-40) · [C-41](#c-41) ·
[C-42](#c-42) · [L1 requirements revised for L2](#l1-requirements-revised-for-l2)

## Summary

| Capability | Phase | IDs | Stories | Contracts written | Complete |
|---|---|---|---|---|---|
| C-01 Project skeleton and releases | Foundation | 20 | S-001, S-002, S-003, S-004, S-097 | yes | 20 of 20 |
| C-02 Process startup and runtime | Foundation | 36 | S-005, S-006, S-007, S-008, S-009, S-098, S-101 | yes | 36 of 36 |
| C-03 Sign-in and access | Foundation | 58 | S-010, S-011, S-012, S-013, S-062, S-014, S-015, S-072 | yes | 58 of 58 |
| C-04 API tokens | Foundation | 17 | S-016, S-017 | yes | 17 of 17 |
| C-05 Integrations | Observation | 18 | S-018, S-019, S-096 | yes | 18 of 18 |
| C-06 Snapshot processing | Observation | 39 | S-020, S-021, S-022, S-065, S-096 | yes | 39 of 39 |
| C-07 Heartbeat | Observation | 12 | S-023, S-024 | yes | 12 of 12 |
| C-08 Routing | Observation | 22 | S-025, S-026, S-027 | yes | 22 of 22 |
| C-09 Alert Group lifecycle | Observation | 51 | S-028, S-029, S-030, S-031, S-065, S-095 | yes | 51 of 51 |
| C-10 Commands | Observation | 41 | S-032, S-063, S-033, S-092, S-093, S-094, S-100 | yes | 41 of 41 |
| C-11 Delivery engine | Shadow | 40 | S-034, S-035 | yes | 40 of 40 |
| C-12 Messages | Shadow | 19 | S-036, S-037, S-038, S-082, S-098, S-099, S-102 | yes | 19 of 19 |
| C-13 Mattermost | Shadow | 31 | S-039, S-061, S-040, S-064, S-087, S-105, S-103 | yes | 31 of 31 |
| C-14 Telegram | Shadow | 36 | S-041, S-042, S-066, S-067, S-043 | yes | 36 of 36 |
| C-15 Outgoing webhook | Shadow | 25 | S-044, S-045, S-046 | yes | 25 of 25 |
| C-16 Destination test | Shadow | 14 | S-047, S-048, S-099 | yes | 14 of 14 |
| C-17 Timers | Actions | 20 | S-049, S-068, S-050 | yes | 20 of 20 |
| C-18 Account links | Actions | 18 | S-051, S-052, S-069, S-070, S-100, S-102 | yes | 18 of 18 |
| C-19 Self-observation | Operations | 15 | S-053, S-054, S-059, S-108 | yes | 15 of 15 |
| C-20 Organization settings and security | Operations | 14 | S-055, S-071, S-056, S-072, S-092, S-094 | yes | 14 of 14 |
| C-21 Documentation site and runbooks | Operations | 20 | S-057, S-058, S-060, S-091, S-106, S-107, S-108, S-109, S-110 | yes | 20 of 20 |
| C-22 Bridge | Bridge | 52 | S-073, S-080, S-074, S-081, S-075, S-076, S-077, S-078, S-083, S-085, S-086, S-079, S-084, S-093 | yes | 52 of 52 |
| C-23 Request log | Bridge | 13 | S-088, S-089, S-090, S-097, S-100, S-101 | yes | 13 of 13 |
| C-24 Slack | Slack | 37 | S-097, S-098, S-099, S-100, S-101, S-102, S-105, S-103, S-104 | yes | 37 of 37 |
| C-25 Teams | L2 Teams | 26 | S-115, S-116, S-117, S-118, S-163, S-121, S-170, S-119, S-120, S-124, S-125, S-131, S-133, S-135, S-138, S-139, S-140, S-171, S-141, S-142, S-143, S-144, S-154, S-169, S-158 | yes | 26 of 26 |
| C-26 Routing to Teams and by Severity | L2 Teams | 24 | S-111, S-162, S-112, S-121, S-170, S-122, S-119, S-123, S-124, S-140, S-169 | yes | 24 of 24 |
| C-27 Schedules | L2 On call | 24 | S-131, S-132, S-133, S-164, S-134, S-135, S-136, S-137, S-150, S-154, S-172 | yes | 24 of 24 |
| C-28 Mentions and buttons of a Destination | L2 Teams, L2 On call | 20 | S-113, S-114, S-138, S-139, S-140, S-171, S-165, S-141, S-142, S-143, S-144 | yes | 20 of 20 |
| C-29 Compact message layout | L2 Teams | 16 | S-125, S-126, S-127, S-128, S-129, S-130, S-168 | yes | 16 of 16 |
| C-30 Notification gateways | L2 Paging | 18 | S-145, S-167, S-146, S-147, S-148, S-173, S-149, S-150, S-151, S-153, S-154, S-157, S-158 | yes | 18 of 18 |
| C-31 Notification rules | L2 Paging | 12 | S-152, S-153, S-155, S-157, S-161 | yes | 12 of 12 |
| C-32 Escalation chains and Assignees | L2 Paging | 38 | S-148, S-154, S-169, S-155, S-177, S-178, S-168, S-156, S-157, S-158, S-159, S-160, S-161 | yes | 38 of 38 |
| C-33 Terraform provider | L2 Terraform provider | 37 | S-179, S-180, S-181, S-182, S-183, S-184, S-185, S-186, S-187, S-188, S-189 | yes | 37 of 37 |
| C-34 Maintenance windows | L2 0.2.x | 41 | S-190, S-191, S-192, S-193, S-194, S-195, S-196 | yes | 41 of 41 |
| C-36 Loud and Quiet settings of a Route | L2 0.2.x | 21 | S-197, S-198, S-199, S-200, S-201 | yes | 21 of 21 |
| C-37 Attach and Detach | L2 0.2.x | 29 | S-202, S-203, S-204, S-205, S-206, S-207, S-208 | yes | 29 of 29 |
| C-38 Message layout editor | L2 0.2.x | 29 | S-209, S-210, S-211, S-212, S-213, S-214, S-215, S-216, S-217, S-218, S-219 | yes | 29 of 29 |
| C-39 Theme switch | L2 0.2.x | 13 | S-220, S-221 | yes | 13 of 13 |
| C-40 A dashboard on the Home page | L2 0.2.x | 21 | S-222, S-223, S-224, S-225, S-226, S-227 | yes | 21 of 21 |
| C-41 An editor of whole Notification rule sequences | L2 0.2.x | 21 | S-228, S-229, S-230 | yes | 21 of 21 |
| C-42 The step-by-step Destination test | L2 0.2.x | 22 | S-231, S-232, S-233, S-234, S-235, S-236, S-237, S-238, S-239 | yes | 22 of 22 |

## C-01

[Project skeleton and releases](../prd/l1/C-01-project-skeleton-and-releases.md) · Foundation · stories: S-001, S-002,
S-003, S-004

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
| C-01.FR-13 | S-004 (part), S-006 (part), S-009 (part), S-013 (part), S-018 (part), S-020 (part), S-023 (part), S-037 (part), S-039 (part), S-041 (part), S-042 (part), S-066 (part), S-067 (part), S-044 (part), S-045 (part), S-047 (part), S-069 (part), S-073 (part), S-097 (part) | complete | later capabilities extend their fake servers and the demo configuration; the fake bridge in S-073; re-verified with several Connections of one messenger in S-069 (D305); the fake Slack server in S-097 |
| C-01.FR-14 | S-001, S-058 (part) | complete | the README demo animation is added after the frontend stories, with S-058 |
| C-01.FR-15 | S-003 (part), S-004 (part) | complete |  |
| C-01.AC-1 | S-001 (part), S-002 (part) | complete |  |
| C-01.AC-2 | S-003, S-006 | complete | verified by S-006: the installed chart becomes ready only once migrations, probes and serving exist |
| C-01.AC-3 | S-003 | complete |  |
| C-01.AC-4 | S-004 | complete |  |
| C-01.AC-5 | S-003 | complete |  |

## C-02

[Process startup and runtime](../prd/l1/C-02-process-startup-and-runtime.md) · Foundation · stories: S-005, S-006,
S-007, S-008, S-009

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
| C-02.FR-10 | S-008 (part), S-023 (part), S-041 (part), S-053 (part), S-101 (part) | complete | later Leader tasks join with S-023, S-041 and S-053; Slack's Socket Mode connections in S-101 |
| C-02.FR-11 | S-008 | complete |  |
| C-02.FR-12 | S-008 (part), S-028 (part), S-049 (part) | complete | collapsed overdue timers and Timeline entries are C-09 and C-17 (S-028, S-049) |
| C-02.FR-13 | S-008 | complete |  |
| C-02.FR-14 | S-006 (part), S-008 (part), S-061 (part), S-041 (part), S-042 (part), S-098 (part) | complete | Connection and Destination checks join with S-061, S-041 and S-042; Slack Connections and Destinations in S-098 |
| C-02.FR-15 | S-006 (part), S-008 (part), S-011 (part), S-012 (part), S-021 (part), S-071 (part) | complete | the other subcommands come with S-011, S-012, S-021 and S-071 |
| C-02.FR-16 | S-006 | complete |  |
| C-02.FR-17 | S-005 | complete |  |
| C-02.FR-18 | S-005 | complete |  |
| C-02.FR-19 | S-005 (part), S-006 (part), S-008 (part), S-009 (part), S-010 (part) | complete |  |
| C-02.FR-20 | S-009, S-101 (part) | complete | WebSocket connections, for Slack's Socket Mode, in S-101 |
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

[Sign-in and access](../prd/l1/C-03-sign-in-and-access.md) · Foundation · stories: S-010, S-011, S-012, S-013, S-062,
S-014, S-015, S-072

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
| C-03.FR-18 | S-012 (part), S-014, S-015 (part), S-072 (part) | complete |  |
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
| C-04.FR-8 | S-016 (part), S-017, S-055 (part), S-056 (part) | complete | editing the grace period is S-055 in the API and S-056 on the Security page (C-20.FR-1) |
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

[Integrations](../prd/l1/C-05-integrations.md) · Observation · stories: S-018, S-019, S-096

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
| C-05.FR-11 | S-018, S-096 (part) | complete | connecting Grafana directly and through an external Alertmanager, added with D383 |
| C-05.FR-12 | S-018 | complete |  |
| C-05.AC-1 | S-018 | complete |  |
| C-05.AC-2 | S-018 | complete |  |
| C-05.AC-3 | S-018 (part), S-019 | complete |  |
| C-05.AC-5 | S-018 | complete |  |
| C-05.AC-6 | S-018 (part), S-019 | complete |  |
| C-05.AC-7 | S-018 | complete |  |
| C-05.AC-8 | S-018 (part), S-020 | complete | verified by S-020: the state `failed` needs processing (C-06); S-018 checks the stored bytes |

## C-06

[Snapshot processing](../prd/l1/C-06-snapshot-processing.md) · Observation · stories: S-020, S-021, S-022, S-065, S-096

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-06.FR-1 | S-020, S-065 (part) | complete | S-065 changes the order from per Integration to per Alertmanager group |
| C-06.FR-2 | S-020 | complete |  |
| C-06.FR-3 | S-020 (part), S-028 | complete | the warning in the Timeline needs Alert Groups (C-09, S-028) |
| C-06.FR-4 | S-020 (part), S-028, S-096 (part) | complete | resolving in the latest Alert Group needs Alert Groups (C-09, S-028); the resolve of an overlapping firing comes with S-096 (D383) |
| C-06.FR-21 | S-020, S-096 (part) | complete | the truncated Snapshot from Grafana comes with S-096 (D383) |
| C-06.FR-5 | S-020 | complete |  |
| C-06.FR-6 | S-020 (part), S-021, S-023 | complete | the time-based end of truncation needs the liveness clock of C-07 (S-023) |
| C-06.FR-7 | S-020 (part), S-023 | complete | Stale in each `groupKey` needs the Heartbeat of C-07 (S-023) |
| C-06.FR-8 | S-020 (part), S-023, S-096 (part) | complete | the Stale scan needs the Heartbeat of C-07 (S-023); Snapshots from Grafana, without `notification_reason`, in S-096 |
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
| C-06.FR-20 | S-020 (part), S-021, S-065 (part) | complete | S-065 retries transient failures in the Snapshot's lane and keeps a failed Snapshot from holding back its lane |
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
| C-06.AC-16 | S-096 | complete | added with D383 |
| C-06.AC-17 | S-096 | complete |  |
| C-06.AC-18 | S-096 | complete |  |

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
| C-08.FR-1 | S-025 (part), S-027 (part), S-031 (part), S-033 (part), S-034 (part), S-035 (part), S-036 (part), S-038 (part), S-064 (part), S-050 (part), S-070 (part) | complete | later policy sections and the Destinations picker come with their capabilities (S-031, S-033, S-038, S-064, S-050); S-035 writes the Destinations of a Route; the Route editor's Destination labels and duplicate-chat warning in S-070 (D309) |
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

[Alert Group lifecycle](../prd/l1/C-09-alert-group-lifecycle.md) · Observation · stories: S-028, S-029, S-030, S-031,
S-065, S-095

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
| C-09.FR-11 | S-028 (part), S-030 (part), S-032 (part), S-063 (part), S-034 (part), S-049 (part), S-070 (part) | complete | each later capability adds its entry types (S-032, S-063, S-034, S-049); the Timeline's delivery entries' tooltips in S-070 (D309) |
| C-09.FR-12 | S-028 (part), S-063 | complete | Snooze ends come with S-063 |
| C-09.FR-18 | S-028 | complete |  |
| C-09.FR-19 | S-028 (part), S-031, S-035 (part) | complete |  |
| C-09.FR-22 | S-028 (part), S-063 (part) | complete |  |
| C-09.FR-13 | S-029 (part), S-030, S-063 (part), S-033, S-061 (part), S-064 (part), S-049 (part), S-050 (part) | complete | later filters come with S-063, S-064 and S-050 |
| C-09.FR-17 | S-030 | complete |  |
| C-09.FR-24 | S-030 (part), S-033 | complete |  |
| C-09.FR-25 | S-012 (part), S-029 (part), S-030 | complete | S-012 brought the stream forward with the notice and Organization hints |
| C-09.FR-26 | S-095 | complete | added with D379, after the Bridge phase |
| C-09.FR-14 | S-028 (part), S-030, S-033, S-034 (part), S-037 (part), S-038 (part), S-064 (part), S-050 (part), S-070 (part) | complete | later sections come with S-038, S-064 and S-050; the delivery section's tooltips in S-070 (D309) |
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
| C-09.AC-26 | S-095 | complete |  |

## C-10

[Commands](../prd/l1/C-10-commands.md) · Observation · stories: S-032, S-063, S-033, S-092, S-093, S-094

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-10.FR-1 | S-032 (part), S-063 (part), S-033 | complete |  |
| C-10.FR-2 | S-032 (part), S-033 | complete |  |
| C-10.FR-3 | S-032, S-061 (part), S-067 (part), S-100 (part) | complete | the messenger Transports are passed in by S-061 and S-067; the Transport `slack` in S-100 |
| C-10.FR-4 | S-032 (part), S-033, S-049 (part) | complete | Reminders starting over for the new Owner are C-17 (S-049) |
| C-10.FR-5 | S-032 | complete |  |
| C-10.FR-6 | S-032 (part), S-063 (part), S-033, S-061 (part), S-042 (part), S-067 (part) | complete | the messenger durations come with S-061, and with S-042 (buttons) and S-067 (presses) |
| C-10.FR-7 | S-032 (part), S-033 | complete |  |
| C-10.FR-8 | S-063 (part), S-033 | complete |  |
| C-10.FR-10 | S-032 | complete |  |
| C-10.FR-11 | S-032 (part), S-061 (part), S-067 (part), S-051 | complete | the messenger presses are S-061 and S-067; refusals of presses without an Account link are verified with real links by S-051 |
| C-10.FR-12 | S-032 (part), S-063 (part) | complete |  |
| C-10.FR-13 | S-063 (part), S-033 | complete |  |
| C-10.FR-14 | S-032 (part), S-033 | complete |  |
| C-10.FR-15 | S-032 (part), S-063 (part) | complete |  |
| C-10.FR-16 | S-032 (part), S-063 (part), S-033, S-049 (part), S-050 (part) | complete | `still_on_it` is C-17 (S-049, S-050) |
| C-10.FR-17 | S-092 | complete | added with D379, after the Bridge phase |
| C-10.FR-18 | S-092 (part), S-093 (part), S-094 (part) | complete | the API (S-092), the buttons and Text commands of the messengers of C-13, C-14 and C-22 (S-093), the dialog (S-094) |
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
| C-10.AC-17 | S-032, S-092 (part) | complete | the third choice `preset` comes with S-092 |
| C-10.AC-18 | S-032 (part), S-063 (part) | complete |  |
| C-10.AC-19 | S-063 | complete |  |
| C-10.AC-20 | S-063 | complete |  |
| C-10.AC-21 | S-092 | complete |  |
| C-10.AC-22 | S-092 | complete |  |
| C-10.AC-23 | S-093 | complete | verified with the messengers of C-13 and C-14, which S-093 follows |
| C-10.AC-24 | S-094 | complete |  |

## C-11

[Delivery engine](../prd/l1/C-11-delivery-engine.md) · Shadow · stories: S-034, S-035

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-11.FR-1 | S-034, S-069 (part), S-070 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305); two Destinations in one chat are warned about and `channel_chat_id` is read in S-070 (D309) |
| C-11.FR-2 | S-034 (part), S-039 (part), S-061 (part), S-047 (part) | complete | its callers: checks from S-039, ephemeral answers from S-061, `limited` test steps from S-047 |
| C-11.FR-3 | S-034 | complete |  |
| C-11.FR-4 | S-034 | complete |  |
| C-11.FR-5 | S-034 | complete |  |
| C-11.FR-6 | S-035 (part), S-044 (part), S-045 (part) | complete |  |
| C-11.FR-7 | S-034 (part), S-061 (part), S-042 (part), S-066 (part) | complete |  |
| C-11.FR-8 | S-034 (part), S-035 (part), S-061 (part), S-042 (part), S-044 (part) | complete |  |
| C-11.FR-9 | S-035 (part), S-061 (part), S-064 (part), S-042 (part), S-044 (part), S-047 (part) | complete |  |
| C-11.FR-10 | S-035, S-064 (part) | complete |  |
| C-11.FR-11 | S-035 | complete |  |
| C-11.FR-12 | S-035 | complete |  |
| C-11.FR-13 | S-035 | complete |  |
| C-11.FR-14 | S-035 (part), S-039 (part), S-044 (part), S-045 (part) | complete |  |
| C-11.FR-15 | S-034 | complete |  |
| C-11.FR-16 | S-034 (part), S-035 (part), S-064 (part), S-066 (part), S-070 (part) | complete | the delivery section's tooltips in S-070 (D309) |
| C-11.FR-17 | S-034 (part), S-035 (part) | complete |  |
| C-11.FR-18 | S-034 (part), S-035 (part), S-039 (part), S-042 (part), S-044 (part) | complete |  |
| C-11.FR-19 | S-035 (part), S-044 (part) | complete |  |
| C-11.FR-20 | S-034 (part), S-035 (part), S-049 (part) | complete | delivery handles the C-17 rows from S-034 on; they are produced from S-049 on |
| C-11.FR-21 | S-034 (part), S-035 (part), S-066 (part) | complete |  |
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
| C-11.AC-15 | S-034 | complete | added by pull request #533 (issue #504), which also revised C-11.FR-3; checked by the e2e test of that pull request, not by a step of S-034 |
| C-11.AC-16 | S-034 | complete | added by pull request #539 (issue #534), which also revised C-11.FR-3; checked by the integration and unit tests of that pull request, not by a step of S-034 |
| C-11.AC-17 | S-034 | complete | added by pull request #544 (issue #540), which implements C-11.FR-3 as revised; checked by the unit and integration tests of that pull request, not by a step of S-034 |
| C-11.AC-18 | S-034 | complete | proposed and implemented by pull request #553 (issue #546), corrected by pull request #559 (issue #554); checked by the unit and integration tests of #553 and #559, not by a step of S-034 |
| C-11.AC-19 | S-034 | complete | added by pull request #559 (issues #554 and #547), which also revised C-11.FR-3; checked by the unit and integration tests of that pull request, not by a step of S-034 |

## C-12

[Messages](../prd/l1/C-12-messages.md) · Shadow · stories: S-036, S-037, S-038, S-082

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-12.FR-1 | S-036 (part), S-037 (part), S-061 (part), S-042 (part) | complete |  |
| C-12.FR-2 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-3 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-4 | S-036 (part), S-037 (part), S-045 (part), S-049 (part) | complete | the ack timeout notice is rendered from S-049 on |
| C-12.FR-5 | S-036 (part), S-038 (part) | complete |  |
| C-12.FR-6 | S-036 (part), S-037 (part), S-038 (part) | complete |  |
| C-12.FR-7 | S-036 (part), S-037 (part), S-061 (part), S-042 (part), S-099 (part) | complete | mrkdwn for Slack in S-099 |
| C-12.FR-8 | S-037 (part), S-039 (part), S-061 (part), S-064 (part), S-042 (part), S-066 (part), S-044 (part), S-045 (part), S-098 (part) | complete | what a Slack Destination takes in S-098 |
| C-12.FR-9 | S-037 (part), S-038 (part) | complete |  |
| C-12.FR-10 | S-036 | complete |  |
| C-12.FR-11 | S-036 (part), S-061 (part), S-042 (part) | complete |  |
| C-12.FR-12 | S-037, S-051 (part), S-102 (part) | complete | checked with real Account links in C-18.AC-7 (S-051); Slack's Mention form in S-102; the footer's Mentions since the revision of journal D521 |
| C-12.FR-13 | S-036 (part), S-037 (part), S-045 (part) | complete |  |
| C-12.AC-1 | S-036 (part), S-037 (part) | complete |  |
| C-12.AC-2 | S-036 (part), S-038 (part) | complete |  |
| C-12.AC-3 | S-036, S-061 (part) | complete | through the recording test adapter; repeated against the fake Mattermost server in S-061 |
| C-12.AC-4 | S-036 | complete |  |
| C-12.AC-5 | S-036 | complete | the default message through previews; Thread replies through the recording test adapter and in S-061 |
| C-12.AC-6 | S-037 (part), S-038 (part) | complete |  |

## C-13

[Mattermost](../prd/l1/C-13-mattermost.md) · Shadow · stories: S-039, S-061, S-040, S-064, S-087

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-13.FR-1 | S-039 (part), S-040 (part), S-070 (part) | complete | the Connections list shows the Mattermost server in S-070 (D309) |
| C-13.FR-2 | S-039 (part), S-040 (part), S-061 (part), S-064 (part), S-087 (part) | complete | the warning `press_answers_in_thread` in S-061, shown on the Connection page in S-040; the dry probe in S-087 (D353) |
| C-13.FR-3 | S-039 (part), S-061 (part) | complete |  |
| C-13.FR-4 | S-061 (part), S-068 (part), S-069 (part) | complete | presses from linked accounts are tested with link rows set up directly; Account links are created from S-051; presses on Thread replies come with the Reminder buttons of S-068; re-verified with several Connections of one messenger in S-069 (D305) |
| C-13.FR-5 | S-061 | complete |  |
| C-13.FR-6 | S-039 | complete |  |
| C-13.FR-7 | S-061 | complete |  |
| C-13.FR-8 | S-061 | complete |  |
| C-13.FR-9 | S-064, S-070 (part) | complete | the Destinations list's "Connection · channel" column in S-070 (D309) |
| C-13.FR-10 | S-039 (part), S-061 (part), S-064 (part) | complete |  |
| C-13.FR-11 | S-061 (part), S-064 (part), S-070 (part) | complete | the Internal-alerts suggestion picker's labels in S-070 (D309) |
| C-13.FR-12 | S-061 (part), S-064 (part) | complete |  |
| C-13.FR-13 | S-039 (part), S-040 (part), S-047 (part), S-048 (part) | complete |  |
| C-13.FR-14 | S-087, S-105 (part), S-103 (part) | complete | added after the Bridge (D353): the check before saving of every Connection type; Slack, added with C-24 (D381): the operation in S-105, the forms in S-103 |
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
| C-13.AC-17 | S-087 | complete |  |

## C-14

[Telegram](../prd/l1/C-14-telegram.md) · Shadow · stories: S-041, S-042, S-066, S-067, S-043

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-14.FR-1 | S-041 (part), S-043 (part), S-070 (part) | complete | the Connections list shows the Telegram bot in S-070 (D309) |
| C-14.FR-2 | S-042 (part), S-066 (part), S-043 (part), S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |
| C-14.FR-3 | S-066, S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |
| C-14.FR-4 | S-067, S-068 (part), S-069 (part) | complete | presses from linked accounts are tested with link rows set up directly; Account links are created from S-051; presses on Thread replies come with the Reminder buttons of S-068; re-verified with several Connections of one messenger in S-069 (D305) |
| C-14.FR-5 | S-067 | complete | the answer to a linked account is checked with link rows set up directly (S-051 creates links) |
| C-14.FR-6 | S-042 (part), S-066 (part) | complete |  |
| C-14.FR-7 | S-042 | complete |  |
| C-14.FR-8 | S-041 (part), S-051 | complete | the router receives `/start`; the bot answers it once Account links exist (S-051) |
| C-14.FR-9 | S-042 (part), S-058 | complete | the section on restricted networks is C-21.FR-7 (S-058) |
| C-14.FR-10 | S-041 (part), S-043 (part) | complete |  |
| C-14.FR-11 | S-041 (part), S-043 (part), S-087 (part) | complete | a token typed into a check before saving in S-087 (D353) |
| C-14.FR-12 | S-041 (part), S-042 (part) | complete |  |
| C-14.FR-13 | S-042 | complete |  |
| C-14.FR-14 | S-042 (part), S-043 (part) | complete |  |
| C-14.FR-15 | S-042 | complete |  |
| C-14.FR-16 | S-042 | complete |  |
| C-14.AC-1 | S-066 | complete |  |
| C-14.AC-2 | S-066 | complete |  |
| C-14.AC-3 | S-041 | complete |  |
| C-14.AC-4 | S-067 | complete | by the press-age rule of C-14.FR-4 |
| C-14.AC-5 | S-041 | complete |  |
| C-14.AC-6 | S-041 (part), S-043 (part), S-087 (part) | complete | with no token typed again, in S-087 |
| C-14.AC-7 | S-041 (part), S-042 (part), S-043 (part) | complete |  |
| C-14.AC-8 | S-042 | complete |  |
| C-14.AC-9 | S-067 | complete |  |
| C-14.AC-10 | S-041 | complete |  |
| C-14.AC-11 | S-041 (part), S-067 (part) | complete |  |
| C-14.AC-12 | S-042 | complete |  |
| C-14.AC-13 | S-042 (part), S-043 (part), S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |
| C-14.AC-14 | S-042 | complete |  |
| C-14.AC-15 | S-066 | complete |  |
| C-14.AC-16 | S-066 (part), S-043 (part) | complete |  |
| C-14.AC-17 | S-042 | complete |  |
| C-14.AC-18 | S-067 | complete |  |
| C-14.AC-19 | S-066 | complete |  |
| C-14.AC-20 | S-087 | complete |  |

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
| C-16.FR-3 | S-047 (part), S-048 (part), S-099 (part) | complete | the test of a Slack Destination, which cannot press, in S-099 |
| C-16.FR-4 | S-047 (part), S-048 (part) | complete |  |
| C-16.FR-5 | S-047 | complete |  |
| C-16.FR-6 | S-047 | complete | a scope rule: a test is one "create" request, nothing more; the step-by-step test of the full lifecycle is specified in C-42 (S-231 to S-239) |
| C-16.FR-7 | S-047 | complete |  |
| C-16.AC-1 | S-047 | complete |  |
| C-16.AC-2 | S-047 | complete |  |
| C-16.AC-3 | S-047 (part), S-048 (part) | complete |  |
| C-16.AC-4 | S-047 | complete |  |
| C-16.AC-5 | S-047 | complete |  |
| C-16.AC-6 | S-047 | complete |  |
| C-16.AC-7 | S-047 (part), S-048 (part) | complete |  |

## C-17

[Timers](../prd/l1/C-17-timers.md) · Actions · stories: S-049, S-068, S-050

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-17.FR-1 | S-049 | complete |  |
| C-17.FR-2 | S-049 (part), S-050 (part), S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |
| C-17.FR-3 | S-049 (part), S-050 (part) | complete |  |
| C-17.FR-4 | S-049 (part), S-068 (part), S-069 (part) | complete | the buttons on Reminders are S-068; re-verified with several Connections of one messenger in S-069 (D305) |
| C-17.FR-5 | S-049 | complete |  |
| C-17.FR-6 | S-068, S-071 (part) | complete | S-071 repeats the expiry after a real key rotation |
| C-17.FR-7 | S-049 | complete |  |
| C-17.FR-8 | S-049 | complete | the release itself is S-063 (C-03.FR-13) |
| C-17.FR-9 | S-049 (part), S-050 (part) | complete |  |
| C-17.FR-10 | S-049 (part), S-068 (part), S-050 (part), S-051 (part) | complete | the API with S-049, the messenger buttons with S-068, presses from links made through the profile with S-051 |
| C-17.FR-11 | S-049 | complete |  |
| C-17.AC-1 | S-049 | complete |  |
| C-17.AC-2 | S-049 | complete |  |
| C-17.AC-3 | S-049 | complete |  |
| C-17.AC-4 | S-049 | complete |  |
| C-17.AC-5 | S-049 | complete |  |
| C-17.AC-6 | S-049 (part), S-050 (part) | complete |  |
| C-17.AC-7 | S-068 | complete |  |
| C-17.AC-8 | S-049 (part), S-050 (part) | complete |  |
| C-17.AC-9 | S-049 | complete |  |

## C-18

[Account links](../prd/l1/C-18-account-links.md) · Actions · stories: S-051, S-052, S-069, S-070

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-18.FR-1 | S-051 | complete |  |
| C-18.FR-2 | S-051 (part), S-052 (part), S-069 (part), S-070 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305); "Relink Telegram" in S-070 (D309) |
| C-18.FR-3 | S-051 (part), S-052 (part) | complete |  |
| C-18.FR-4 | S-051, S-069 (part), S-070 (part), S-102 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305); the Identity spaces explained in the profile in S-070 (D309); the Identity space of a Slack Connection in S-102 |
| C-18.FR-5 | S-051 | complete |  |
| C-18.FR-6 | S-051 (part), S-052 (part), S-070 (part) | complete | the user page's Telegram row in S-070 (D309) |
| C-18.FR-7 | S-051 | complete |  |
| C-18.FR-8 | S-051, S-069 (part), S-100 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305); Slack presses in S-100 |
| C-18.FR-9 | S-051 | complete |  |
| C-18.FR-10 | S-051 (part), S-052 (part), S-069 (part), S-070 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305); the profile's Telegram row and the Mattermost hint in S-070 (D309) |
| C-18.FR-11 | S-051 | complete |  |
| C-18.AC-1 | S-051 | complete |  |
| C-18.AC-2 | S-051 | complete |  |
| C-18.AC-3 | S-051, S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |
| C-18.AC-4 | S-051 | complete |  |
| C-18.AC-5 | S-051 | complete | the Reminder buttons are C-17 (S-068) |
| C-18.AC-6 | S-051 | complete |  |
| C-18.AC-7 | S-051, S-069 (part) | complete | re-verified with several Connections of one messenger in S-069 (D305) |

## C-19

[Self-observation](../prd/l1/C-19-self-observation.md) · Operations · stories: S-053, S-054, S-059, S-108

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-19.FR-2 | S-053 | complete |  |
| C-19.FR-3 | S-059 (part), S-060 (part) | complete | the rules are in the chart with S-059; the bypass route is documented by S-060 |
| C-19.FR-4 | S-059 | complete |  |
| C-19.FR-5 | S-053 | complete |  |
| C-19.FR-7 | S-053 (part), S-054 (part) | complete |  |
| C-19.FR-9 | S-053 (part), S-058 (part), S-059, S-108 (part) | complete | Internal alerts S-053, pages S-058, chart rules S-059; `latest` for snapshot and pre-release versions S-108 |
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

[Organization settings and security](../prd/l1/C-20-organization-settings-and-security.md) · Operations · stories:
S-055, S-071, S-056, S-072, S-092, S-094

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-20.FR-1 | S-055 (part), S-056 (part), S-072 (part) | complete |  |
| C-20.FR-2 | S-055 | complete |  |
| C-20.FR-3 | S-055 (part), S-056 (part) | complete |  |
| C-20.FR-6 | S-071 (part), S-056 (part) | complete |  |
| C-20.FR-7 | S-071 | complete |  |
| C-20.FR-8 | S-055 | complete |  |
| C-20.FR-9 | S-092 (part), S-094 (part) | complete | added with the Snooze presets (D379): the API in S-092, the General page in S-094 |
| C-20.AC-1 | S-055 | complete |  |
| C-20.AC-2 | S-071 (part), S-056 (part) | complete |  |
| C-20.AC-3 | S-055 | complete |  |
| C-20.AC-4 | S-055 (part), S-056 (part) | complete |  |
| C-20.AC-5 | S-071 | complete |  |
| C-20.AC-6 | S-055 | complete |  |
| C-20.AC-7 | S-092 | complete |  |

## C-21

[Documentation site and runbooks](../prd/l1/C-21-documentation-site-and-runbooks.md) · Operations · stories: S-057,
S-058, S-060, S-091, S-106, S-107, S-108, S-109, S-110

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-21.FR-1 | S-057 | complete |  |
| C-21.FR-2 | S-057 (part), S-058 (part), S-060 (part) | complete |  |
| C-21.FR-3 | S-058 (part), S-059 | complete | rendered chart rules join the one-to-one check with S-059 |
| C-21.FR-4 | S-057 | complete |  |
| C-21.FR-5 | S-057 (part), S-058 (part), S-060 (part) | complete |  |
| C-21.FR-7 | S-058 | complete |  |
| C-21.FR-8 | S-091 | complete |  |
| C-21.FR-9 | S-106 (part), S-107 (part), S-110 (part) | complete | the tutorial and the messenger pages S-106, the other pages S-107, every screenshot taken again with the links S-110 |
| C-21.FR-10 | S-106 | complete |  |
| C-21.FR-11 | S-108 (part), S-109 (part), S-110 (part) | complete | the version, the component and the main page titles S-108; fields and problems S-109; the admin pages S-110 |
| C-21.FR-12 | S-108 | complete |  |
| C-21.AC-1 | S-057 (part), S-058 (part), S-059 | complete | verified by S-059: the chart's `runbook_url`s exist once the chart renders its rules |
| C-21.AC-2 | S-060 | complete |  |
| C-21.AC-3 | S-058 | complete |  |
| C-21.AC-4 | S-091 | complete |  |
| C-21.AC-5 | S-106 | complete |  |
| C-21.AC-6 | S-106 (part), S-107 (part) | complete |  |
| C-21.AC-7 | S-108 | complete |  |
| C-21.AC-8 | S-108 (part), S-109 (part), S-110 (part) | complete | complete with S-110, after which every place of reference.md's Documentation links shows its link |
| C-21.AC-9 | S-108 | complete |  |

## C-22

[Bridge](../prd/l1/C-22-bridge.md) · Bridge · stories: S-073, S-080, S-074, S-081, S-075, S-076, S-077, S-078, S-083,
S-085, S-086, S-079, S-084, S-093

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-22.FR-1 | S-073 (part), S-077 (part), S-087 (part) | complete | the token rule of a check before saving in S-087 |
| C-22.FR-2 | S-073 (part), S-074 (part), S-077 (part), S-087 (part) | complete | the dry probe in S-087 |
| C-22.FR-3 | S-080 (part), S-074 (part), S-077 (part) | complete |  |
| C-22.FR-4 | S-074 | complete |  |
| C-22.FR-5 | S-074, S-093 (part) | complete | the Snooze presets and their place in the drop order come with S-093 |
| C-22.FR-6 | S-074 | complete |  |
| C-22.FR-7 | S-074 | complete |  |
| C-22.FR-8 | S-073 (part), S-074 (part), S-081 (part) | complete |  |
| C-22.FR-9 | S-081 (part), S-077 (part) | complete |  |
| C-22.FR-10 | S-075 | complete |  |
| C-22.FR-11 | S-075 | complete |  |
| C-22.FR-12 | S-075 (part), S-077 (part) | complete |  |
| C-22.FR-13 | S-075 (part), S-076 (part) | complete |  |
| C-22.FR-14 | S-075, S-093 (part) | complete | the words of the Snooze presets come with S-093 |
| C-22.FR-15 | S-076 (part), S-077 (part) | complete |  |
| C-22.FR-16 | S-076 | complete |  |
| C-22.FR-17 | S-074 (part), S-075 (part), S-077 (part) | complete |  |
| C-22.FR-18 | S-077 | complete |  |
| C-22.FR-19 | S-074 | complete |  |
| C-22.FR-20 | S-073 (part), S-079 (part) | complete |  |
| C-22.FR-21 | S-078 | complete |  |
| C-22.FR-22 | S-085 (part), S-086 (part) | complete | the deployment on the test environment is the maintainer's (S-085) |
| C-22.FR-23 | S-074 (part), S-081 (part), S-076 (part), S-079 (part), S-060 (part) | complete |  |
| C-22.FR-24 | S-073 (part), S-081 (part), S-075 (part) | complete |  |
| C-22.FR-25 | S-083 (part), S-084 (part) | complete |  |
| C-22.AC-1 | S-073 | complete |  |
| C-22.AC-2 | S-080 | complete |  |
| C-22.AC-3 | S-073 (part), S-080 (part), S-074 (part), S-075 (part) | complete |  |
| C-22.AC-4 | S-074 | complete |  |
| C-22.AC-5 | S-074 | complete |  |
| C-22.AC-6 | S-074 | complete |  |
| C-22.AC-7 | S-074 | complete |  |
| C-22.AC-8 | S-081 | complete |  |
| C-22.AC-9 | S-081 | complete |  |
| C-22.AC-10 | S-074 | complete |  |
| C-22.AC-11 | S-074 | complete |  |
| C-22.AC-12 | S-074 (part), S-075 (part) | complete |  |
| C-22.AC-13 | S-075 | complete |  |
| C-22.AC-14 | S-075 | complete |  |
| C-22.AC-15 | S-075 | complete |  |
| C-22.AC-16 | S-075 | complete |  |
| C-22.AC-17 | S-075 | complete |  |
| C-22.AC-18 | S-076 (part), S-077 (part) | complete |  |
| C-22.AC-19 | S-076 | complete |  |
| C-22.AC-20 | S-080 (part), S-076 (part) | complete |  |
| C-22.AC-21 | S-077 | complete |  |
| C-22.AC-22 | S-078 (part), S-086 (part) | complete |  |
| C-22.AC-23 | S-086 | complete |  |
| C-22.AC-24 | S-079 | complete |  |
| C-22.AC-25 | S-083 (part), S-085 (part), S-084 (part) | complete |  |
| C-22.AC-26 | S-087 | complete |  |
| C-22.AC-27 | S-093 | complete |  |

## C-23

[Request log](../prd/l1/C-23-request-log.md) · Bridge · stories: S-088, S-089, S-090

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-23.FR-1 | S-088 (part), S-089 (part), S-100 (part), S-101 (part) | complete | outbound requests in S-088; inbound ones and the checks before saving of new Connections in S-089; Slack's callback in S-100, Socket Mode envelopes in S-101 |
| C-23.FR-2 | S-088 (part), S-089 (part), S-097 (part), S-100 (part) | complete | the kind and run of checks in S-089; Slack's Web API methods in S-097, `interactive` in S-100 |
| C-23.FR-3 | S-088 (part), S-089 (part), S-100 (part), S-101 (part) | complete | Slack's secrets in S-100 and S-101 |
| C-23.FR-4 | S-088 | complete |  |
| C-23.FR-5 | S-088 (part), S-089 (part) | complete | the run filter and the run on check and test results in S-089 |
| C-23.FR-6 | S-090 | complete |  |
| C-23.FR-7 | S-088 | complete |  |
| C-23.AC-1 | S-088 (part), S-090 (part) | complete | "Requests" on the Alert Group page in S-090 |
| C-23.AC-2 | S-088 (part), S-089 (part) | complete |  |
| C-23.AC-3 | S-088 (part), S-089 (part) | complete |  |
| C-23.AC-4 | S-088 | complete |  |
| C-23.AC-5 | S-088 (part), S-089 (part) | complete |  |
| C-23.AC-6 | S-090 | complete |  |

## C-24

[Slack](../prd/l1/C-24-slack.md) · Slack · stories: S-097, S-098, S-099, S-100, S-101, S-102, S-105, S-103, S-104

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-24.FR-1 | S-097 (part), S-103 (part) | complete |  |
| C-24.FR-2 | S-097 (part), S-101 (part), S-105 (part), S-103 (part) | complete | the check before saving and its dry probe in S-105 |
| C-24.FR-3 | S-098 (part), S-099 (part), S-103 (part) | complete |  |
| C-24.FR-4 | S-099 | complete |  |
| C-24.FR-5 | S-099 (part), S-100 (part) | complete |  |
| C-24.FR-6 | S-098 (part), S-099 (part), S-102 (part) | complete |  |
| C-24.FR-7 | S-099 (part), S-103 (part) | complete |  |
| C-24.FR-8 | S-097 (part), S-099 (part) | complete |  |
| C-24.FR-9 | S-100 (part), S-101 (part) | complete | HTTP in S-100, Socket Mode in S-101 |
| C-24.FR-10 | S-100 | complete |  |
| C-24.FR-11 | S-100 | complete |  |
| C-24.FR-12 | S-102 (part), S-103 (part) | complete |  |
| C-24.FR-13 | S-099 (part), S-100 (part), S-103 (part) | complete | the press on a test message in S-100, the panel's note in S-103 |
| C-24.FR-14 | S-097 (part), S-099 (part), S-100 (part), S-101 (part) | complete |  |
| C-24.FR-15 | S-099 | complete |  |
| C-24.FR-16 | S-104 | complete |  |
| C-24.FR-17 | S-100 (part), S-101 (part) | complete |  |
| C-24.AC-1 | S-097 | complete |  |
| C-24.AC-2 | S-105 (part), S-103 (part) | complete |  |
| C-24.AC-3 | S-097 (part), S-098 (part), S-101 (part) | complete |  |
| C-24.AC-4 | S-098 (part) | complete |  |
| C-24.AC-5 | S-099 | complete |  |
| C-24.AC-6 | S-099 | complete |  |
| C-24.AC-7 | S-099 | complete |  |
| C-24.AC-8 | S-099 | complete |  |
| C-24.AC-9 | S-099 (part), S-103 (part) | complete |  |
| C-24.AC-10 | S-101 | complete |  |
| C-24.AC-11 | S-100 | complete |  |
| C-24.AC-12 | S-100 | complete |  |
| C-24.AC-13 | S-100 | complete |  |
| C-24.AC-14 | S-102 | complete |  |
| C-24.AC-15 | S-098 (part), S-099 (part), S-102 (part) | complete |  |
| C-24.AC-16 | S-099 (part), S-100 (part), S-103 (part) | complete |  |
| C-24.AC-17 | S-100 (part), S-101 (part), S-103 (part) | complete |  |
| C-24.AC-18 | S-103 | complete |  |
| C-24.AC-19 | S-100 (part), S-101 (part) | complete | a person's press on the maintainer's test workspace: HTTP in S-100, Socket Mode in S-101 |
| C-24.AC-20 | S-104 | complete |  |

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
| NFR-6 Security | S-001, S-002, S-003, S-007, S-009, S-010, S-012, S-013, S-062, S-016, S-018, S-036, S-041, S-044, S-055, S-071, S-073, S-075, S-087, S-088, S-089, S-097, S-100 | the secret-leak lint (S-001); dependency scanning (S-002); signed images and the SBOM (S-003); AES-256-GCM under the Keyring (S-007) and its rotation (S-071); the outbound address policy, no redirects, per-client proxies (S-009), and a policy change effective at once, idle connections included (S-055); secure cookies, CSRF, the Content Security Policy, argon2id and sign-in throttling (S-010); TOTP (S-012); write-only secret fields and OIDC refusing users without a mapped group (S-013); OIDC re-checks at the IdP (S-062); hashed tokens (S-016, S-018); escaping, neutralized Mentions and signed buttons (S-036); the Telegram token sent only to the saved Bot API address (S-041); outgoing webhook Secrets (S-044); a bridge token sent only to the saved bridge address (S-073); signed pushed bridge events (S-075); a stored token sent only to its saved address and a typed one only to the address typed with it, in the check before saving (S-087); secrets masked in the Request log (S-088, S-089); Slack's tokens sent only to Slack's fixed address (S-097); Slack's presses verified by their signature (S-100) |
| NFR-7 Privacy | S-005, S-011, S-108 | metric labels without alert labels, users or Alert Group numbers (S-005); pseudonymized deletion (S-011); documentation links that load nothing before they are followed and send no `Referer` (S-108); no telemetry and no email are properties of the whole design, kept by review |
| NFR-8 Internationalization | S-014, S-036, S-057 | the UI in English and Russian with plural forms and a lint for missing keys (S-014); built-in message texts in both languages, chosen per Route (S-036); the documentation in English (S-057) |
| NFR-9 Time | S-008, S-010, S-014, S-036, S-060 | the clock skew check (S-008); UTC in the API (S-010); the user's time zone in the UI (S-014); absolute times in the Organization's time zone in messages (S-036); NTP on the nodes in the high-availability page (S-060) |
| NFR-10 Retention | S-008, S-029, S-055 | partitions dropped by retention (S-008); batched deletes of summary rows and details (S-029, with the other owners of unpartitioned tables); editing the periods (S-055) |
| NFR-11 Upgrades | S-003, S-006, S-060 | semantic versions through release-please (S-003); expand/contract migrations and the refusal of a newer schema (S-006); the upgrade page (S-060) |
| NFR-12 Compatibility | S-003, S-006, S-020, S-039, S-041, S-073, S-096, S-097 | binaries for linux and darwin on amd64 and arm64, the chart with Ingress and Gateway API routes (S-003); PostgreSQL 14 or newer and the session-capable pooler check (S-006); `notification_reason` with the fallback to identical Snapshots (S-020); Grafana Alerting's webhook contact point (S-096); Mattermost REST API v4 with a bot account (S-039); the Bot API at a configurable base URL (S-041); any other messenger through version 1 of the Bridge protocol (S-073); Slack through its Web API, with Socket Mode or HTTP (S-097) |
| NFR-13 API | S-002, S-010, S-011 | spec-first generation (S-002); RFC 9457 errors with pointers, UTC and `public_id` in URLs (S-010); optimistic locking with `ETag` and `If-Match`, and cursor pagination without `OFFSET` (S-011) |
| NFR-14 Quality | S-001, S-002, S-004, S-014 | the coverage gate (S-001); the nightly mutation report (S-002); end-to-end tests with fake servers (S-004) and a real browser (S-014); evidence for every acceptance statement in each pull request (section 7 of the [README](README.md#7-lifecycle)) |
| NFR-15 Phone width | S-030 | the Alert Group list and page at 360 CSS pixels, checked end to end at that width |

### Changes of L2

The [non-functional requirements of L2](../prd/L2.md#3-non-functional-requirements) change some of L1's; the stories
of the L2 core that own each change:

| NFR | Owned by | What each story contributes |
|---|---|---|
| NFR-2 Latency, NFR-4 Availability | S-133, S-145, S-148, S-149, S-155, S-169 | the Leader's scan for gaps (S-133) and gauge of Broken gateways (S-149), its only additions; the events of a gateway in the poll mode through Bridge polling (S-145); calls claimed by any replica (S-148); Pages by Mention as Thread replies of delivery (S-155); Escalation steps as timer rows that any replica claims, so a lost Leader never stops a chain (S-169) |
| NFR-6 Security | S-134, S-167, S-173 | the iCal token hashed, shown once and masked where its URL would be logged (S-134); a gateway contact set only by its person from the web session and confirmed by a code through the gateway (S-167); an acknowledgement through a gateway acting as the person Muster's signed action data names (S-173) |
| NFR-7 Privacy | S-115, S-131, S-133, S-149, S-154, S-155, S-167 | Teams, Schedules, chains and gateways in metric labels by their ids (S-115, S-131, S-133, S-149, S-154, S-155); gateway contacts erased with their User and masked in the Request log (S-167) |
| NFR-8 Internationalization | S-143, S-148, S-173, S-155, S-168 | the texts of undelivered Mentions (S-143), calls (S-148) and their answers (S-173), Pages and undelivered Pages (S-155) and Assignees (S-168) in English and Russian |
| NFR-11 Upgrades | S-111, S-121, S-125, S-140, S-162 | the urgent mark and "critical is Urgent" stopped, read-only until 0.3.0 (S-111); `destination_ids` derived (S-121); the default layout changed (S-125); the Mention settings moved into entries (S-140); the profile On-call renamed Paging (S-162); each in the upgrade notes of 0.2.0 |
| NFR-12 Compatibility | S-126, S-139, S-171, S-145 | the optional scope `usergroups:read` and group mentions in `info` (S-139); `Mention.kind` (S-171); `layout` (S-126); the roles `messenger` and `gateway` (S-145) — all additive in version 1 of the Bridge protocol |
| NFR-15 Phone width | S-120, S-136, S-159 | the Teams on the Alert Group page (S-120); the Home page (S-136); the Assignees and running chains on the Alert Group page and "I am an Assignee" (S-159), at 360 CSS pixels |

## C-25

[Teams](../prd/l2/C-25-teams.md) · L2 Teams, L2 On call (Team mentions), L2 Paging (chains) · stories: S-115, S-116,
S-117, S-118, S-163, S-121, S-170, S-119, S-120, S-124, S-125, S-131, S-133, S-135, S-138, S-139, S-140, S-171, S-141,
S-142, S-143, S-144, S-154, S-169, S-158

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-25.FR-1 | S-115 (part), S-117 (part), S-118 (part), S-131 (part), S-139 (part), S-154 (part) | complete | the Destinations of a Team are C-26 (S-121); its Escalation chains S-154 |
| C-25.FR-2 | S-115 (part), S-118 (part), S-119 (part), S-120 (part), S-131 (part), S-135 (part), S-154 (part), S-158 (part) | complete | the owning Team of Escalation chains and the filter of their list (S-154, S-158) |
| C-25.FR-3 | S-115 (part), S-119 (part) | complete |  |
| C-25.FR-4 | S-116 (part), S-117 (part), S-119 (part) | complete | GitLab's role claims checked by the spike S-116 |
| C-25.FR-5 | S-118 (part), S-163 (part), S-120 (part), S-133 (part) | complete | the Team's slug under the Team label on `MusterScheduleGap` (S-133) |
| C-25.FR-6 | S-118 (part), S-120 (part), S-125 (part) | complete |  |
| C-25.FR-7 | S-118 (part), S-163 (part), S-120 (part) | complete |  |
| C-25.FR-8 | S-138 (part), S-139 (part), S-140 (part), S-171 (part), S-141 (part), S-142 (part), S-143 (part) | complete | the facts of the spike S-138; Mentions of the Team in messages S-140, in Slack and through bridges S-171; the bridge libraries S-141, S-142 |
| C-25.FR-9 | S-115 (part), S-119 (part), S-154 (part), S-169 (part) | complete | Escalation steps limited to Working hours: the chain's owning Team's (S-154), skipped outside them (S-169) |
| C-25.FR-10 | S-115 (part), S-118 (part), S-170 (part), S-131 (part), S-154 (part) | complete | the refusal `team_in_use` for chains (S-154); P-64 confirmed with S-131 |
| C-25.FR-11 | S-115 (part), S-117 (part), S-118 (part), S-139 (part) | complete |  |
| C-25.FR-12 | S-115 (part), S-117 (part), S-118 (part), S-124 (part), S-139 (part), S-144 (part) | complete |  |
| C-25.FR-13 | S-163 (part), S-121 (part) | complete |  |
| C-25.AC-1 | S-118 (part), S-163 (part) | complete |  |
| C-25.AC-2 | S-118 | complete |  |
| C-25.AC-3 | S-117 | complete |  |
| C-25.AC-4 | S-117 | complete | the fake OIDC server's GitLab mode follows S-116's facts |
| C-25.AC-5 | S-118 | complete |  |
| C-25.AC-6 | S-115 | complete |  |
| C-25.AC-7 | S-139 | complete |  |
| C-25.AC-8 | S-115 | complete |  |
| C-25.AC-9 | S-115 (part), S-118 (part), S-131 (part) | complete |  |
| C-25.AC-10 | S-119 (part), S-120 (part), S-143 (part) | complete |  |
| C-25.AC-11 | S-115 (part), S-118 (part), S-119 (part), S-120 (part) | complete |  |
| C-25.AC-12 | S-115 | complete |  |
| C-25.AC-13 | S-163 (part), S-121 (part) | complete | "starts no Escalation chain" holds while chains do not run; S-169 keeps it (C-32.AC-18) |

## C-26

[Routing to Teams and by Severity](../prd/l2/C-26-routing-to-teams-and-by-severity.md) · L2 Teams · stories: S-111,
S-162, S-112, S-121, S-170, S-122, S-119, S-123, S-124, S-140, S-169

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-26.FR-1 | S-121 (part), S-123 (part), S-140 (part) | complete | the Mention entry "Team {Team}" that naming a Destination of the Team adds is C-28.FR-6, S-140 |
| C-26.FR-2 | S-121 (part), S-123 (part), S-169 (part) | complete | "Run the Teams' Escalation chains" acts on chains with C-32.FR-4 (S-169) |
| C-26.FR-3 | S-121 | complete |  |
| C-26.FR-4 | S-121 | complete |  |
| C-26.FR-5 | S-121 (part), S-123 (part), S-140 (part), S-192 (part), S-198 (part), S-203 (part) | complete | the Mentions of the Destination's entries for `new_alert_group` (S-140); `destination_joined` waits for the end of a Maintenance window (S-192); it is Quiet where the Route does not list `new_alert_group` (S-198) and while the Alert Group is attached (S-203) |
| C-26.FR-6 | S-170 | complete |  |
| C-26.FR-7 | S-121 (part), S-119 (part) | complete |  |
| C-26.FR-8 | S-111 (part), S-112 (part) | complete |  |
| C-26.FR-9 | S-111 (part), S-112 (part), S-121 (part) | complete | "starts no Escalation chain" holds while chains do not run; S-169 keeps it (C-32.AC-18) |
| C-26.FR-10 | S-111 (part), S-112 (part) | complete |  |
| C-26.FR-11 | S-162 | complete |  |
| C-26.FR-12 | S-122 (part), S-123 (part) | complete |  |
| C-26.FR-13 | S-111 (part), S-121 (part), S-170 (part), S-122 (part), S-124 (part) | complete |  |
| C-26.AC-1 | S-121 | complete |  |
| C-26.AC-2 | S-121 | complete |  |
| C-26.AC-3 | S-121 | complete |  |
| C-26.AC-4 | S-121 | complete |  |
| C-26.AC-5 | S-121 | complete |  |
| C-26.AC-6 | S-170 | complete |  |
| C-26.AC-7 | S-111 | complete |  |
| C-26.AC-8 | S-111 (part), S-162 (part) | complete |  |
| C-26.AC-9 | S-123 | complete |  |
| C-26.AC-10 | S-121 (part), S-170 (part) | complete |  |
| C-26.AC-11 | S-111 (part), S-121 (part) | complete |  |

## C-27

[Schedules](../prd/l2/C-27-schedules.md) · L2 On call · stories: S-131, S-132, S-133, S-164, S-134, S-135, S-136, S-137,
S-150, S-154, S-172

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-27.FR-1 | S-131 (part), S-135 (part), S-136 (part) | complete | P-90 confirmed with S-131 |
| C-27.FR-2 | S-131 (part), S-135 (part) | complete |  |
| C-27.FR-3 | S-131 (part), S-135 (part) | complete | P-65 and P-91 confirmed with S-131 |
| C-27.FR-4 | S-131 (part), S-135 (part) | complete |  |
| C-27.FR-5 | S-131 (part), S-135 (part) | complete |  |
| C-27.FR-6 | S-131 (part), S-135 (part) | complete |  |
| C-27.FR-7 | S-133 (part), S-135 (part) | complete | P-80 until the test environment; P-88 decided |
| C-27.FR-8 | S-132 (part), S-164 (part), S-135 (part), S-136 (part), S-150 (part), S-172 (part) | complete | the Destinations of the Team (S-164); whether a gateway call still reaches the person, in the API and on the pages (S-150); the Destinations of the chains that Page the Schedule (S-172) |
| C-27.FR-9 | S-133 (part), S-164 (part), S-136 (part) | complete |  |
| C-27.FR-10 | S-134 (part), S-136 (part) | complete | P-82 confirmed with S-134; P-81 stays provisional |
| C-27.FR-11 | S-131 | complete |  |
| C-27.FR-12 | S-131 (part), S-154 (part) | complete | `schedule_in_use` raised for Escalation steps (S-154); P-66 confirmed with S-131 |
| C-27.FR-13 | S-131 (part), S-135 (part) | complete |  |
| C-27.FR-14 | S-131 (part), S-133 (part), S-164 (part), S-134 (part), S-137 (part) | complete |  |
| C-27.AC-1 | S-131 | complete |  |
| C-27.AC-2 | S-131 (part), S-135 (part) | complete |  |
| C-27.AC-3 | S-131 | complete |  |
| C-27.AC-4 | S-131 | complete |  |
| C-27.AC-5 | S-133 | complete |  |
| C-27.AC-6 | S-164 (part), S-135 (part), S-136 (part) | complete |  |
| C-27.AC-7 | S-134 | complete |  |
| C-27.AC-8 | S-131 (part), S-133 (part), S-154 (part) | complete | `schedule_in_use` for a Schedule that an Escalation step names (S-154) |
| C-27.AC-9 | S-135 (part), S-136 (part) | complete |  |
| C-27.AC-10 | S-131 | complete |  |

## C-28

[Mentions and buttons of a Destination](../prd/l2/C-28-mentions-and-buttons-of-a-destination.md) · L2 Teams (buttons),
L2 On call (Mentions) · stories: S-113, S-114, S-138, S-139, S-140, S-171, S-165, S-141, S-142, S-143, S-144

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-28.FR-1 | S-140 (part), S-143 (part) | complete |  |
| C-28.FR-2 | S-138 (part), S-139 (part), S-140 (part), S-171 (part), S-141 (part), S-142 (part), S-143 (part) | complete | the facts of the spike S-138; what each Connection can mention S-139; the validation S-140; writing groups in Slack, and groups and everyone in bridges, S-171; the bridge libraries S-141, S-142 |
| C-28.FR-3 | S-140 (part), S-165 (part), S-143 (part) | complete |  |
| C-28.FR-4 | S-140, S-198 (part) | complete | an entry for a Loud event that the Route makes Quiet mentions nobody there (S-198) |
| C-28.FR-5 | S-140 | complete |  |
| C-28.FR-6 | S-140 | complete |  |
| C-28.FR-7 | S-140 | complete |  |
| C-28.FR-8 | S-113 (part), S-114 (part) | complete |  |
| C-28.FR-9 | S-113 | complete |  |
| C-28.FR-10 | S-113 (part), S-114 (part), S-140 (part), S-143 (part) | complete |  |
| C-28.FR-11 | S-114 (part), S-144 (part) | complete |  |
| C-28.AC-1 | S-140 | complete |  |
| C-28.AC-2 | S-140 | complete |  |
| C-28.AC-3 | S-140 (part), S-165 (part) | complete |  |
| C-28.AC-4 | S-165 | complete |  |
| C-28.AC-5 | S-140 | complete |  |
| C-28.AC-6 | S-140 | complete |  |
| C-28.AC-7 | S-113 | complete |  |
| C-28.AC-8 | S-140 | complete |  |
| C-28.AC-9 | S-114 (part), S-143 (part) | complete |  |

## C-29

[Compact message layout](../prd/l2/C-29-compact-message-layout.md) · L2 Teams · stories: S-125, S-126, S-127, S-128,
S-129, S-130, S-168

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-29.FR-1 | S-125 (part), S-126 (part), S-168 (part) | complete | the Assignees in item 7, and "· Escalation chains stopped" after the Owner (S-168) |
| C-29.FR-2 | S-125 (part), S-129 (part) | complete |  |
| C-29.FR-3 | S-125 | complete | P-67 stays provisional until the test environment confirms it |
| C-29.FR-4 | S-125 (part), S-126 (part) | complete |  |
| C-29.FR-5 | S-125 | complete |  |
| C-29.FR-6 | S-125 (part), S-126 (part) | complete |  |
| C-29.FR-7 | S-125 | complete |  |
| C-29.FR-8 | S-126 (part), S-127 (part), S-128 (part) | complete | the libraries in `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python` |
| C-29.FR-9 | S-125 (part), S-130 (part) | complete |  |
| C-29.AC-1 | S-125 | complete |  |
| C-29.AC-2 | S-125 | complete |  |
| C-29.AC-3 | S-125 | complete |  |
| C-29.AC-4 | S-125 | complete |  |
| C-29.AC-5 | S-125 | complete |  |
| C-29.AC-6 | S-126 | complete |  |
| C-29.AC-7 | S-129 | complete |  |

## C-30

[Notification gateways](../prd/l2/C-30-notification-gateways.md) · L2 Paging · stories: S-145, S-167, S-146, S-147,
S-148, S-173, S-149, S-150, S-151, S-153, S-154, S-157, S-158

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-30.FR-1 | S-145 (part), S-150 (part) | complete |  |
| C-30.FR-2 | S-145 (part), S-146 (part), S-147 (part), S-148 (part), S-173 (part) | complete | the protocol, the check and the fake gateway (S-145); the libraries in `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python` (S-146, S-147); the event acting on Alert Groups (S-173) |
| C-30.FR-3 | S-167 (part), S-150 (part) | complete | P-86 stays provisional until the test environment confirms it |
| C-30.FR-4 | S-148 (part), S-157 (part) | complete | the queue (S-148); the calls of steps and rules (S-157); P-87 stays provisional until the test environment confirms it |
| C-30.FR-5 | S-145 (part), S-148 (part) | complete |  |
| C-30.FR-6 | S-173, S-203 (part) | complete | an acknowledgement for an attached Alert Group is answered as one for an Alert Group someone owns (S-203) |
| C-30.FR-7 | S-148 (part), S-157 (part) | complete |  |
| C-30.FR-8 | S-149 (part), S-150 (part) | complete | P-88, for the chart rules of C-27 and C-30, decided |
| C-30.FR-9 | S-167 (part), S-150 (part), S-153 (part), S-154 (part), S-157 (part), S-158 (part) | complete | the profile (S-150), the rules (S-153), the steps (S-154, S-157) and the chain form (S-158) |
| C-30.FR-10 | S-145 (part), S-167 (part), S-148 (part), S-173 (part) | complete |  |
| C-30.FR-11 | S-151 | complete | until S-160 settles the push of Mentions, the page says that at night only a Notification gateway is reliable |
| C-30.AC-1 | S-145 (part), S-150 (part) | complete |  |
| C-30.AC-2 | S-167 | complete |  |
| C-30.AC-3 | S-148 | complete |  |
| C-30.AC-4 | S-173 | complete |  |
| C-30.AC-5 | S-149 | complete |  |
| C-30.AC-6 | S-150 | complete |  |
| C-30.AC-7 | S-148 | complete |  |

## C-31

[Notification rules](../prd/l2/C-31-notification-rules.md) · L2 Paging · stories: S-152, S-153, S-155, S-157, S-161

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-31.FR-1 | S-152 | complete | P-85 decided |
| C-31.FR-2 | S-152 | complete |  |
| C-31.FR-3 | S-155 (part), S-157 (part) | complete | Mention actions with the Pages of S-155, call actions S-157 |
| C-31.FR-4 | S-152 (part), S-153 (part) | complete |  |
| C-31.FR-5 | S-152 (part), S-153 (part), S-228 (part), S-229 (part), S-230 (part) | complete | the switch (S-152, S-153); the editor of whole sequences in the profile and on a User's page (S-228, S-229) is specified in C-41, with the revision marker in S-230 |
| C-31.FR-6 | S-161 | complete |  |
| C-31.AC-1 | S-155 | complete |  |
| C-31.AC-2 | S-157 | complete |  |
| C-31.AC-3 | S-152 | complete |  |
| C-31.AC-4 | S-152 | complete |  |
| C-31.AC-5 | S-153 | complete |  |
| C-31.AC-6 | S-152 (part), S-153 (part), S-157 (part) | complete | the API (S-152), the profile (S-153), the call at once (S-157) |

## C-32

[Escalation chains and Assignees](../prd/l2/C-32-escalation-chains-and-assignees.md) · L2 Paging · stories: S-148,
S-154, S-169, S-155, S-177, S-178, S-168, S-156, S-157, S-158, S-159, S-160, S-161

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-32.FR-1 | S-154 (part), S-158 (part) | complete | P-84 confirmed with S-154 |
| C-32.FR-2 | S-154 (part), S-158 (part) | complete |  |
| C-32.FR-3 | S-154 (part), S-169 (part), S-155 (part), S-156 (part), S-157 (part), S-158 (part) | complete |  |
| C-32.FR-4 | S-169 | complete | P-70 confirmed with S-169 |
| C-32.FR-5 | S-169, S-193 (part), S-199 (part), S-204 (part) | complete | the rows of a Maintenance window: becoming kept back stops the chains, the end starts them over (S-193); new Alerts that remove an acknowledgement start them over, where the Route says so (S-199); an Attach stops them and a Detach of a firing Alert Group starts them over (S-204) |
| C-32.FR-6 | S-169 | complete |  |
| C-32.FR-7 | S-169 | complete |  |
| C-32.FR-8 | S-155 (part), S-177 (part) | complete | Mattermost and Slack (S-155), Telegram, Bridges and outgoing webhooks (S-177) |
| C-32.FR-9 | S-155 (part), S-177 (part) | complete | Mattermost and Slack (S-155), Telegram and Bridges (S-177) |
| C-32.FR-10 | S-155 (part), S-157 (part) | complete |  |
| C-32.FR-11 | S-155 (part), S-177 (part), S-157 (part), S-160 (part), S-161 (part) | complete | the push of Mentions in Thread replies checked by the spike S-160 |
| C-32.FR-12 | S-168 (part), S-157 (part) | complete | P-89 decided with S-168: the Owner is named without a Team; Assignees by calls S-157 |
| C-32.FR-13 | S-156 | complete |  |
| C-32.FR-14 | S-169 (part), S-156 (part), S-157 (part) | complete |  |
| C-32.FR-15 | S-169 | complete |  |
| C-32.FR-16 | S-169 | complete | P-73 confirmed with S-169 |
| C-32.FR-17 | S-154 (part), S-158 (part) | complete |  |
| C-32.FR-18 | S-169 (part), S-168 (part), S-178 (part), S-159 (part) | complete |  |
| C-32.FR-19 | S-148 (part), S-154 (part), S-155 (part), S-178 (part) | complete |  |
| C-32.FR-20 | S-161 | complete | cites the facts of S-160 |
| C-32.AC-1 | S-155 | complete |  |
| C-32.AC-2 | S-168 | complete |  |
| C-32.AC-3 | S-169 | complete |  |
| C-32.AC-4 | S-169 | complete |  |
| C-32.AC-5 | S-169 | complete |  |
| C-32.AC-6 | S-155 (part), S-168 (part) | complete |  |
| C-32.AC-7 | S-155 (part), S-157 (part) | complete | the Mention steps (S-155), the gateway step (S-157) |
| C-32.AC-8 | S-169 | complete |  |
| C-32.AC-9 | S-156 | complete |  |
| C-32.AC-10 | S-168 (part), S-159 (part) | complete |  |
| C-32.AC-11 | S-155 (part), S-168 (part), S-156 (part) | complete | with S-155, S-168 and S-156 every row of the L2 lifecycle event table that C-32 records |
| C-32.AC-12 | S-169 | complete |  |
| C-32.AC-13 | S-155 (part), S-168 (part) | complete |  |
| C-32.AC-14 | S-158 (part), S-159 (part) | complete | the chain page (S-158), the Alert Group page at 360 pixels (S-159) |
| C-32.AC-15 | S-154 (part), S-169 (part) | complete |  |
| C-32.AC-16 | S-169 (part), S-155 (part) | complete |  |
| C-32.AC-17 | S-169 (part), S-178 (part), S-158 (part) | complete | the Pages of the runs (S-178) |
| C-32.AC-18 | S-169 | complete |  |

## C-33

[Terraform provider](../prd/l2/C-33-terraform-provider.md) · L2 Terraform provider · stories: S-179, S-180, S-181,
S-182, S-183, S-184, S-185, S-186, S-187, S-188, S-189. S-182 to S-188 are worked in
`muster-io/terraform-provider-muster`.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-33.FR-1 | S-182 | complete |  |
| C-33.FR-2 | S-179 (part), S-182 (part) | complete | the licence in Muster (S-179); the copy, its check and the client (S-182) |
| C-33.FR-3 | S-182 | complete |  |
| C-33.FR-4 | S-182 | complete | 0.2.0, which carries S-179 and S-180 |
| C-33.FR-5 | S-183 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete | by group of resources |
| C-33.FR-6 | S-184 (part), S-186 (part) | complete | `muster_connection` (S-184); `muster_user`, `muster_team` (S-186) |
| C-33.FR-7 | S-183 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.FR-8 | S-182 (part), S-184 (part), S-186 (part), S-187 (part) | complete | the machinery (S-182), the `412` test (S-184), the shared objects: a Team (S-186, S-187) and the Route order (S-187) |
| C-33.FR-9 | S-183 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.FR-10 | S-183 (part), S-184 (part), S-185 (part), S-188 (part) | complete | the guide "Secrets" (S-188) |
| C-33.FR-11 | S-179 (part), S-180 (part) | complete |  |
| C-33.FR-12 | S-181 | complete |  |
| C-33.FR-13 | S-183 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.FR-14 | S-183 (part), S-185 (part), S-187 (part) | complete | the settings objects (S-183), "Explore" (S-185), the Default route (S-187) |
| C-33.FR-15 | S-186 | complete |  |
| C-33.FR-16 | S-186 (part), S-187 (part) | complete |  |
| C-33.FR-17 | S-182 (part), S-184 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.FR-18 | S-182 (part), S-188 (part), S-189 (part) | complete | generation (S-182), guides (S-188), Muster's page (S-189) |
| C-33.FR-19 | S-182 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.FR-20 | S-182 (part), S-188 (part) | complete | the pipeline (S-182); the key, the Registries and the release (S-188) |
| C-33.AC-1 | S-182 | complete |  |
| C-33.AC-2 | S-183 (part), S-187 (part) | complete | the complete example (S-187) |
| C-33.AC-3 | S-179 (part), S-180 (part) | complete |  |
| C-33.AC-4 | S-181 | complete |  |
| C-33.AC-5 | S-183 (part), S-184 (part), S-187 (part) | complete |  |
| C-33.AC-6 | S-184 | complete |  |
| C-33.AC-7 | S-183 (part), S-184 (part), S-185 (part), S-186 (part), S-187 (part) | complete |  |
| C-33.AC-8 | S-184 | complete |  |
| C-33.AC-9 | S-184 | complete |  |
| C-33.AC-10 | S-186 | complete |  |
| C-33.AC-11 | S-186 (part), S-187 (part) | complete |  |
| C-33.AC-12 | S-183 | complete |  |
| C-33.AC-13 | S-184 (part), S-187 (part) | complete |  |
| C-33.AC-14 | S-186 (part), S-187 (part) | complete |  |
| C-33.AC-15 | S-182 | complete |  |
| C-33.AC-16 | S-188 | complete |  |
| C-33.AC-17 | S-188 (part), S-189 (part) | complete |  |

## C-34

[Maintenance windows](../prd/l2/C-34-maintenance-windows.md) · L2 0.2.x · stories: S-190, S-191, S-192, S-193, S-194,
S-195, S-196. They start after the tag of 0.2.0 and ship in a 0.2.x patch release.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-34.FR-1 | S-190 | complete |  |
| C-34.FR-2 | S-190 | complete |  |
| C-34.FR-3 | S-190 | complete |  |
| C-34.FR-4 | S-190 (part), S-194 (part) | complete | the API's times (S-190); the profile's time zone in the form (S-194) |
| C-34.FR-5 | S-190 (part), S-191 (part) | complete | the Audit log of windows (S-190); the start and end as events of the Alert Groups (S-191) |
| C-34.FR-6 | S-191 | complete |  |
| C-34.FR-7 | S-191 (part), S-193 (part) | complete | the rule (S-191); a joining Team leaves running chains alone (S-193) |
| C-34.FR-8 | S-191 | complete |  |
| C-34.FR-9 | S-191 | complete |  |
| C-34.FR-10 | S-191 (part), S-192 (part) | complete | ingestion, status and Commands (S-191); the buttons of Root messages (S-192) |
| C-34.FR-11 | S-192 | complete |  |
| C-34.FR-12 | S-192 | complete |  |
| C-34.FR-13 | S-191 (part), S-192 (part), S-195 (part) | complete | the events and the Timeline (S-191); their messenger and webhook effects (S-192); the texts (S-195) |
| C-34.FR-14 | S-193 | complete |  |
| C-34.FR-15 | S-193 | complete |  |
| C-34.FR-16 | S-191 | complete |  |
| C-34.FR-17 | S-192 (part), S-195 (part) | complete | the rule (S-192); the delivery state's text (S-195) |
| C-34.FR-18 | S-192 (part), S-193 (part) | complete | the publication with the mark (S-192); the chains and the ack timeout (S-193) |
| C-34.FR-19 | S-192 (part), S-193 (part) | complete | the Thread reply and `destination_joined` (S-192); the chains starting over (S-193) |
| C-34.FR-20 | S-194 (part), S-195 (part) | complete | the sections, banner and list (S-194); the Alert Group page and list (S-195) |
| C-34.FR-21 | S-190 (part), S-191 (part), S-192 (part) | complete | the resource (S-190); `kept_back` on Alert Groups (S-191); in webhook events (S-192) |
| C-34.FR-22 | S-190 (part), S-191 (part), S-192 (part) | complete | log events (S-190), `muster_maintenance_kept_back_total` (S-191), `muster_maintenance_released_total` (S-192); what the telemetry category reads is the rows of `maintenance_windows` (S-190), and reporting it belongs to the opt-in telemetry item of L2 §1.5, a question for the maintainer (Open question 1 of S-191) |
| C-34.FR-23 | S-196 | complete | the page and its CI example; the statistics count kept-back Alert Groups as any other, with no code of their own; the Terraform resource `muster_maintenance_window` is in no story of the provider (S-182 to S-189), a question for the maintainer (Open question 1 of S-196) |
| C-34.AC-1 | S-190 (part), S-194 (part) | complete | the Service account, the Responder who closes through the API, the Viewer and the refusals (S-190); the close from the UI (S-194) |
| C-34.AC-2 | S-190 | complete |  |
| C-34.AC-3 | S-191 (part), S-192 (part), S-193 (part) | complete | the state and Timeline (S-191); nothing delivered, no Storm (S-192); no chain step (S-193) |
| C-34.AC-4 | S-191 (part), S-192 (part) | complete | the Quiet `maintenance_ended` (S-191); "Not posted" (S-192) |
| C-34.AC-5 | S-192 (part), S-193 (part) | complete | the publications and the Storm (S-192); the chain and the ack timeout notice (S-193) |
| C-34.AC-6 | S-193 | complete |  |
| C-34.AC-7 | S-191 | complete |  |
| C-34.AC-8 | S-191 (part), S-193 (part) | complete | the rule and the event (S-191); the chains left running (S-193) |
| C-34.AC-9 | S-190 (part), S-191 (part), S-192 (part), S-193 (part) | complete | the times (S-190); the decision (S-191); the Root message and Thread reply (S-192); the chain (S-193) |
| C-34.AC-10 | S-193 | complete |  |
| C-34.AC-11 | S-191 (part), S-192 (part) | complete | the Snooze and the Command from the API (S-191); the button of a Root message (S-192) |
| C-34.AC-12 | S-191 (part), S-192 (part) | complete | the release (S-191); the Loud publication (S-192) |
| C-34.AC-13 | S-192 | complete |  |
| C-34.AC-14 | S-192 (part), S-193 (part) | complete | the events (S-192); no chain until the end (S-193) |
| C-34.AC-15 | S-191 | complete |  |
| C-34.AC-16 | S-194 (part), S-195 (part) | complete | the Team, Integration and list pages (S-194); the Alert Group page and list (S-195) |
| C-34.AC-17 | S-191 (part), S-192 (part) | complete | `muster_maintenance_kept_back_total` (S-191); `muster_maintenance_released_total` (S-192); the log events (S-190, S-191) |
| C-34.AC-18 | S-191 | complete |  |

## C-36

[Loud and Quiet settings of a Route](../prd/l2/C-36-route-loud-and-quiet-settings.md) · L2 0.2.x · stories: S-197,
S-198, S-199, S-200, S-201. They start after the tag of 0.2.0 and ship in a 0.2.x patch release.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-36.FR-1 | S-197 (part), S-198 (part) | complete | the fields, the API and the Audit log (S-197); that edits apply at once to the events recorded after them (S-198) |
| C-36.FR-2 | S-198 (part), S-199 (part) | complete | every row of the table (S-198); the row `alerts_added` (removes) (S-199) |
| C-36.FR-3 | S-198 | complete |  |
| C-36.FR-4 | S-198 | complete |  |
| C-36.FR-5 | S-198 | complete |  |
| C-36.FR-6 | S-199 | complete |  |
| C-36.FR-7 | S-199 | complete |  |
| C-36.FR-8 | S-197 | complete |  |
| C-36.FR-9 | S-197 (part), S-200 (part) | complete | the stored value (S-197); the unavailable box that ticks when the ack timeout is turned on (S-200) |
| C-36.FR-10 | S-197 (part), S-201 (part) | complete | the migration and the optional input (S-197); the upgrade note that suggests unticking events (S-201) |
| C-36.FR-11 | S-200 (part), S-201 (part) | complete | the form's link (S-200); the page (S-201) |
| C-36.AC-1 | S-197 (part), S-200 (part) | complete | the profiles and the stored Route (S-197); the form filled from the profile (S-200) |
| C-36.AC-2 | S-198 | complete |  |
| C-36.AC-3 | S-198 (part), S-199 (part) | complete | the rows of the table (S-198); the removal row (S-199) |
| C-36.AC-4 | S-198 | complete |  |
| C-36.AC-5 | S-199 | complete |  |
| C-36.AC-6 | S-199 | complete |  |
| C-36.AC-7 | S-199 | complete |  |
| C-36.AC-8 | S-197 | complete |  |
| C-36.AC-9 | S-198 | complete |  |
| C-36.AC-10 | S-200 | complete |  |

## C-37

[Attach and Detach](../prd/l2/C-37-attach-and-detach.md) · L2 0.2.x · stories: S-202, S-203, S-204, S-205, S-206, S-207,
S-208. They start after the tag of 0.2.0 and ship in a 0.2.x patch release.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-37.FR-1 | S-202 (part), S-205 (part), S-207 (part) | complete | the Commands, Permission and metric (S-202); no button or Text command in messengers (S-205); the UI (S-207); the MCP server is not built yet |
| C-37.FR-2 | S-202 | complete |  |
| C-37.FR-3 | S-202 (part), S-203 (part) | complete | the pair of Attach and Detach (S-202); a main Alert Group with its attached ones (S-203) |
| C-37.FR-4 | S-202 (part), S-204 (part) | complete | the status taken, Assignees and Unclaimed (S-202); chains, ack timeout, Reminders and the end of a Snooze (S-204) |
| C-37.FR-5 | S-203 (part), S-204 (part) | complete | following (S-203); starting no chain or timer of its own (S-204) |
| C-37.FR-6 | S-203 (part), S-204 (part) | complete | the three endings (S-203); the chain and the ack timeout of an Alert Group that the system detaches start over (S-204) |
| C-37.FR-7 | S-203 (part), S-205 (part) | complete | the status, the loudness and the main Alert Group's independence (S-203); the messengers (S-205) |
| C-37.FR-8 | S-202 (part), S-204 (part) | complete | what Detach keeps (S-202); chains, ack timeout and Reminders starting over (S-204) |
| C-37.FR-9 | S-203 (part), S-206 (part) | complete | the Commands from any Transport (S-203); the bulk item (S-206) |
| C-37.FR-10 | S-205 | complete |  |
| C-37.FR-11 | S-205 | complete |  |
| C-37.FR-12 | S-206 (part), S-207 (part) | complete | the API for the page's section and Alerts (S-206); the dialog, notice and section (S-207) |
| C-37.FR-13 | S-206 (part), S-207 (part) | complete | the filter, the count and the bulk Attach in the API (S-206); the mark, the filter control and the bar (S-207) |
| C-37.FR-14 | S-206 (part), S-207 (part) | complete | the statistics (S-206); "Resolved with #{M}" on the page (S-207) |
| C-37.FR-15 | S-202 (part), S-203 (part), S-205 (part), S-206 (part) | complete | the rows and the Commands' entries (S-202), following and the system endings (S-203), the messenger column (S-205), webhook events (S-206) |
| C-37.FR-16 | S-202 | complete |  |
| C-37.FR-17 | S-208 | complete |  |
| C-37.AC-1 | S-202 (part), S-205 (part) | complete | the Timeline, Audit log and API (S-202); the Root message and Thread reply (S-205) |
| C-37.AC-2 | S-204 | complete |  |
| C-37.AC-3 | S-203 | complete |  |
| C-37.AC-4 | S-202 (part), S-203 (part), S-206 (part) | complete | the refusals of FR-2 and `403` (S-202); `alert_group_attached` (S-203); the bulk Acknowledge (S-206) |
| C-37.AC-5 | S-203 (part), S-204 (part), S-205 (part) | complete | the statuses and entries (S-203); the chain at step 1 (S-204); "Resolved with #1287" (S-205) |
| C-37.AC-6 | S-202 (part), S-204 (part), S-205 (part) | complete | the Alert Group kept (S-202); the first Reminder 4 hours later (S-204); the Root message edited back (S-205) |
| C-37.AC-7 | S-203 (part), S-205 (part) | complete | the entries and the status (S-203); the line in a new Destination (S-205) |
| C-37.AC-8 | S-205 | complete |  |
| C-37.AC-9 | S-206 | complete |  |
| C-37.AC-10 | S-206 | complete |  |
| C-37.AC-11 | S-202 (part), S-206 (part) | complete | the limit (S-202); the bulk Attach (S-206) |
| C-37.AC-12 | S-207 | complete |  |

## C-38

[Message layout editor](../prd/l2/C-38-message-layout-editor.md) · L2 0.2.x · stories: S-209, S-210, S-211, S-212,
S-213, S-214, S-215, S-216, S-217, S-218, S-219. They start after the tag of 0.2.0 and ship in 0.2.x patch releases.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-38.FR-1 | S-210 (part), S-211 (part) | complete | the setting, its default and the read (S-210); every Destination of the Route renders it, and the default renders as before (S-211) |
| C-38.FR-2 | S-209 (part), S-210 (part), S-211 (part), S-217 (part) | complete | the limit of fields that the Slack fact settles (S-209); what a layout holds and its checks (S-210); the rendering (S-211); the editor (S-217) |
| C-38.FR-3 | S-211 | complete |  |
| C-38.FR-4 | S-210 | complete |  |
| C-38.FR-5 | S-210 (part), S-217 (part) | complete | the reset and the Audit log (S-210); the reset button and the dropped draft (S-217) |
| C-38.FR-6 | S-209 (part), S-211 (part), S-212 (part), S-213 (part), S-214 (part) | complete | the two facts (S-209); Mattermost, Slack, Telegram and the bridge's body (S-211); the bridge's `layout` with `order` (S-212); the types in the two libraries (S-213, S-214) |
| C-38.FR-7 | S-211 (part), S-216 (part) | complete | Thread replies, Storm summaries and Mentions unchanged (S-211); the test message (S-216) |
| C-38.FR-8 | S-211 | complete | the preview's own `layout` null for a template is S-215's, under AC-6 |
| C-38.FR-9 | S-210 (part), S-217 (part) | complete | the save with the template removed (S-210); the hint and "Remove the template" (S-217) |
| C-38.FR-10 | S-215 | complete |  |
| C-38.FR-11 | S-215 (part), S-218 (part) | complete | the example, an Alert Group of any Route and the refusal of one whose details are removed (S-215); the offered list within `retention.alert_details` and "Find by number" (S-218) |
| C-38.FR-12 | S-215 (part), S-218 (part) | complete | the payload, the length and the cut blocks (S-215); the tabs (S-218) |
| C-38.FR-13 | S-215 | complete | the Redaction clause waits for the capability that specifies the rules (S-215, Open questions) |
| C-38.FR-14 | S-210 (part), S-211 (part) | complete | the re-render row (S-210); the edits, Quietly and once (S-211) |
| C-38.FR-15 | S-210 (part), S-217 (part) | complete | the API and the Permissions (S-210); the read-only editor (S-217) |
| C-38.FR-16 | S-219 | complete |  |
| C-38.FR-17 | S-216 (part), S-218 (part) | complete | the operation (S-216); the editor's button (S-218) |
| C-38.AC-1 | S-210 (part), S-211 (part) | complete | the read-back (S-210); the Root message byte for byte (S-211) |
| C-38.AC-2 | S-211 (part), S-212 (part) | complete | Mattermost, Slack and Telegram (S-211); the fake bridge (S-212) |
| C-38.AC-3 | S-211 | complete |  |
| C-38.AC-4 | S-210 | complete |  |
| C-38.AC-5 | S-211 | complete |  |
| C-38.AC-6 | S-211 (part), S-215 (part) | complete | the rendering (S-211); the preview's `layout` null (S-215) |
| C-38.AC-7 | S-210 (part), S-215 (part), S-218 (part) | complete | the Viewer's read (S-210); the other Route's Alert Group, the example and the `403` (S-215); the offered order (S-218) |
| C-38.AC-8 | S-215, S-218 (part) | complete | the reports (S-215); the length and cut lines in the UI (S-218) |
| C-38.AC-9 | S-216 | complete |  |
| C-38.AC-10 | S-217 (part), S-218 (part) | complete | the blocks, fields, keyboard move, apply and reset (S-217); the preview, its tabs and the apply (S-218) |
| C-38.AC-11 | S-210 (part), S-211 (part), S-212 (part) | complete | the refusals of 0 and 21 (S-210); three values and "…and 9 more" (S-211); the `order` and `muster bridge check` (S-212) |
| C-38.AC-12 | S-210 (part), S-216 (part), S-218 (part) | complete | the one save of the layout and the template's removal (S-210); the test message and the `403` (S-216); the editor's test message (S-218) |

## C-39

[Theme switch](../prd/l2/C-39-theme-switch.md) · L2 0.2.x · stories: S-220, S-221. They start after the tag of 0.2.0
and ship in 0.2.x patch releases.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-39.FR-1 | S-220 (part), S-221 (part) | complete | the value, its default and the existing Users (S-220); `system` following the media query (S-221) |
| C-39.FR-2 | S-220 (part), S-221 (part) | complete | the profile setting, the web-session rule and the Audit log (S-220); the switch, the immediate application and other tabs (S-221) |
| C-39.FR-3 | S-221 | complete |  |
| C-39.FR-4 | S-221 | complete |  |
| C-39.FR-5 | S-221 | complete |  |
| C-39.FR-6 | S-220 | complete |  |
| C-39.FR-7 | S-221 | complete |  |
| C-39.AC-1 | S-220 | complete |  |
| C-39.AC-2 | S-221 | complete |  |
| C-39.AC-3 | S-221 | complete |  |
| C-39.AC-4 | S-221 | complete |  |
| C-39.AC-5 | S-221 | complete |  |
| C-39.AC-6 | S-221 | complete |  |

## C-40

[A dashboard on the Home page](../prd/l2/C-40-home-dashboard.md) · L2 0.2.x · stories: S-222, S-223, S-224, S-225,
S-226, S-227. They start after the tag of 0.2.0 and ship in 0.2.x patch releases; their contracts were written against
the open pull request that adds the capability file (#673), before it merged.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-40.FR-1 | S-225 (part), S-226 (part) | complete | the section, its place and the figures and chart (S-225); the tables (S-226) |
| C-40.FR-2 | S-223 (part), S-225 (part) | complete | the period and the filters accepted by the reads (S-223); the controls, the URL and "Open in Statistics" (S-225) |
| C-40.FR-3 | S-223 (part), S-224 (part), S-225 (part), S-226 (part) | complete | `alert-groups:read` on the reads (S-223, S-224); the visibility of every part to everyone who reads Alert Groups (S-225, S-226) |
| C-40.FR-4 | S-223 (part), S-225 (part) | complete | the totals, medians and percentiles of `group_by=all` (S-223); the figures and the counts of open Alert Groups (S-225) |
| C-40.FR-5 | S-223 (part), S-225 (part) | complete | the series with the critical split and the hours (S-223); the chart (S-225) |
| C-40.FR-6 | S-222 (part), S-224 (part), S-226 (part) | complete | the recording on each Page (S-222); the read and the moment from which it counts (S-224); the table and its hint (S-226) |
| C-40.FR-7 | S-223 (part), S-226 (part) | complete | the titles (S-223); the table and the link to the list (S-226) |
| C-40.FR-8 | S-225 (part), S-226 (part) | complete | the separate reads, errors, refresh and hints (S-225); the tables as parts (S-226) |
| C-40.FR-9 | S-225 (part), S-226 (part) | complete | the empty states of the figures and the chart (S-225) and of the tables (S-226) |
| C-40.FR-10 | S-223 (part), S-224 | complete | the shape of the queries (S-223); P-96's data, the budgets and the indexes (S-224) |
| C-40.FR-11 | S-225 (part), S-226 (part) | complete | the phone layout of the figures (S-225) and of the tables (S-226) |
| C-40.FR-12 | S-227 | complete |  |
| C-40.AC-1 | S-223 (part), S-225 (part) | complete | the figures through the API (S-223) and in the browser (S-225) |
| C-40.AC-2 | S-223 | complete |  |
| C-40.AC-3 | S-225 (part), S-226 (part) | complete | the filter Team, the URL and "Open in Statistics" (S-225); the tables (S-226) |
| C-40.AC-4 | S-222 (part), S-224 (part), S-226 (part) | complete | the value at the moment of the Page (S-222); the numbers through the API (S-224); the table (S-226) |
| C-40.AC-5 | S-223 (part), S-226 (part) | complete | the order through the API (S-223); the table and its link (S-226) |
| C-40.AC-6 | S-223 (part), S-224 (part), S-225 (part), S-226 (part) | complete | the Permission and the lint of the reads (S-223, S-224); the Viewer's page (S-225, S-226) |
| C-40.AC-7 | S-225 (part), S-226 (part) | complete | the empty installation and a failing part (S-225, S-226); the hint reads the counts only (S-225) |
| C-40.AC-8 | S-225 (part), S-226 (part) | complete |  |
| C-40.AC-9 | S-224 | complete | P-96 is confirmed with the measured values, or left provisional with the reason |

## C-41

[An editor of whole Notification rule sequences](../prd/l2/C-41-notification-rule-sequence-editor.md) · L2 0.2.x ·
stories: S-228, S-229, S-230. They start after the tag of 0.2.0 and ship in 0.2.x patch releases; their contracts were
written against the open pull request that adds the capability file (#673), before it merged. The capability adds no
API, so it has no backend story.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-41.FR-1 | S-228 | complete |  |
| C-41.FR-2 | S-228 | complete |  |
| C-41.FR-3 | S-228 (part), S-229 (part) | complete | the profile's texts (S-228); the User page's text and the Organization's gateways (S-229) |
| C-41.FR-4 | S-228 | complete |  |
| C-41.FR-5 | S-228 | complete |  |
| C-41.FR-6 | S-228 | complete |  |
| C-41.FR-7 | S-229 | complete |  |
| C-41.FR-8 | S-228 (part), S-229 (part) | complete | no marker on a person's rules (S-228) and the Organization's warning (S-229) |
| C-41.FR-9 | S-229 | complete |  |
| C-41.FR-10 | S-228 (part), S-229 (part) | complete | the profile (S-228); the User page and Alert handling (S-229) |
| C-41.FR-11 | S-230 | complete | with the revision marker on C-31.FR-5 |
| C-41.AC-1 | S-228 | complete |  |
| C-41.AC-2 | S-228 | complete |  |
| C-41.AC-3 | S-228 (part), S-229 (part) | complete | the profile's text (S-228); the User page's (S-229) |
| C-41.AC-4 | S-228 | complete |  |
| C-41.AC-5 | S-228 | complete |  |
| C-41.AC-6 | S-229 | complete |  |
| C-41.AC-7 | S-228 (part), S-229 (part) | complete |  |
| C-41.AC-8 | S-228 | complete |  |
| C-41.AC-9 | S-228 | complete |  |
| C-41.AC-10 | S-229 | complete |  |

## C-42

[The step-by-step Destination test](../prd/l2/C-42-destination-test.md) · L2 0.2.x · stories: S-231, S-232, S-233,
S-234, S-235, S-236, S-237, S-238, S-239. They start after the tag of 0.2.0 and ship in 0.2.x patch releases; their
contracts were written against the open pull request that adds the capability file (#673), before it merged.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-42.FR-1 | S-231 (part), S-237 (part) | complete | the run, its operations and expiry (S-231); the form and the expiry notice (S-237) |
| C-42.FR-2 | S-231 (part), S-232 (part), S-233 (part), S-237 (part) | complete | the eight steps for Mattermost (S-231), Telegram and Slack (S-232), a Bridge and an outgoing webhook (S-233); the steps on the page (S-237) |
| C-42.FR-3 | S-231 (part), S-232 (part), S-233 (part), S-237 (part) | complete | the interactive path, masking and classes by type; the results and "Try again" on the page (S-237) |
| C-42.FR-4 | S-231 (part), S-232 (part), S-233 (part), S-237 (part) | complete | "Would mention:" and the Mention-free message by type; on the page (S-237) |
| C-42.FR-5 | S-231 (part), S-234, S-237 (part) | complete | the buttons from the button set (S-231); the presses, their answers and their record (S-234); the page (S-237) |
| C-42.FR-6 | S-231 (part), S-232 (part), S-233 (part), S-234 (part) | complete | nothing written or counted by a run, by type and by press |
| C-42.FR-7 | S-236 (part), S-238 (part) | complete | the offers (S-236); the listing on the page (S-238) |
| C-42.FR-8 | S-235 (part), S-236 (part), S-238 (part) | complete | the timeline of a chain (S-235), of all chains (S-236) and on the page (S-238) |
| C-42.FR-9 | S-235 (part), S-236, S-238 (part) | complete | the computation without effects (S-235); no Thread reply in a Test message (S-236); the page's words (S-238) |
| C-42.FR-10 | S-231 (part), S-235 (part), S-236 (part) | complete | the steps' Permission and Audit log (S-231); playing, not recorded (S-235, S-236) |
| C-42.FR-11 | S-231 (part), S-232 (part), S-233 (part), S-237 (part), S-238 (part) | complete | the language of the Test message's texts by type (S-231, S-232, S-233); 360 px and Russian on the page (S-237, S-238) |
| C-42.FR-12 | S-239 | complete |  |
| C-42.AC-1 | S-231, S-237 (part) | complete | the eight steps through the API (S-231) and in the browser (S-237) |
| C-42.AC-2 | S-233 | complete |  |
| C-42.AC-3 | S-234, S-237 (part) | complete | the press answered and recorded (S-234); listed on the page (S-237) |
| C-42.AC-4 | S-231 (part), S-232 (part), S-233 (part), S-237 (part) | complete | the entry that would mention nobody, by type |
| C-42.AC-5 | S-231, S-237 (part) | complete | the expiry through the API (S-231) and on the page (S-237) |
| C-42.AC-6 | S-235 (part), S-236, S-238 (part) | complete | the three-step chain (S-235); all chains (S-236); the page (S-238) |
| C-42.AC-7 | S-235, S-238 (part) | complete |  |
| C-42.AC-8 | S-231 (part), S-235 (part), S-236 (part) | complete | the steps (S-231), playing (S-235, S-236) |
| C-42.AC-9 | S-237 (part), S-238 (part) | complete |  |
| C-42.AC-10 | S-231 (part), S-236 (part) | complete | no Mention markup (S-231); no Thread reply from playing (S-236) |

## L1 requirements revised for L2

The requirements of L1 that [L2 §6](../prd/L2.md#6-revisions-to-l1) revises, and those it adds to L1 files, with the
L2 stories that carry the revision. Their rows in the L1 sections above stay as they were, for L1; a row here is
complete when the revision is. Every L1 ID of L2 §6 has its row, and every row is complete for the capabilities that
have stories; C-43, which has none yet, also revises C-11.FR-2 and C-11.FR-3, and its stories add themselves to those
rows.

| ID | Covered by | Status | Note |
|---|---|---|---|
| C-03.FR-5 | S-116 (part), S-117 (part), S-119 (part) | complete | the claim of every mapping entry and subtrees; GitLab's facts in S-116 |
| C-03.FR-6 | S-117 | complete |  |
| C-03.FR-7 | S-117 | complete |  |
| C-03 UI | S-119 | complete | the claim of each entry in the group mapping editor |
| C-03.AC-28 | S-117 | complete | added for L2 |
| C-03.FR-13 | S-115 (part), S-167 (part) | complete | out of every Team (S-115); the gateway contacts erased (S-167) |
| C-03.FR-14 | S-173 | complete | the Transport `gateway` |
| C-03.FR-30 | S-117 | complete |  |
| C-06.FR-19 | S-118 (part), S-120 (part) | complete |  |
| C-08 scenario 2 | S-111, S-112 | complete | the Minimum severity instead of the urgent mark |
| C-08.FR-1 | S-111 (part), S-115 (part), S-121 (part), S-123 (part), S-125 (part), S-154 (part), S-158 (part), S-197 (part), S-200 (part) | complete | the Escalation chains of a Route (S-154) and on its form (S-158); the Loud and Quiet settings (S-197) and their section of the form (S-200) |
| C-08.FR-6 | S-111 | complete |  |
| C-08.FR-7 | S-162, S-197 (part), S-200 (part) | complete | the profiles' Loud events and removal of an acknowledgement (S-197) pre-fill the section of the form (S-200) |
| C-08 UI | S-112, S-119, S-123, S-129, S-158, S-200 (part) | complete | the Escalation chains on the Route form (S-158); the section "Loud and Quiet" (S-200) |
| C-08.AC-11 | S-162 | complete |  |
| C-09.FR-1 | S-111 (part), S-118 (part), S-169 (part), S-168 (part), S-202 (part), S-203 (part) | complete | the running chains (S-169) and the Assignees (S-168); the attached state (S-202) and following (S-203) |
| C-09.FR-4 | S-198 (part), S-199 (part), S-203 (part) | complete | added for L2: the Reopen into firing as a Loud event of the Route (S-198); a Reopen into acknowledged keeps its Owner and its Alerts remove nothing (S-199); a Reopen never attaches again (S-203) |
| C-09.FR-6 | S-198 (part), S-199 (part), S-203 (part) | complete | added for L2: new Alerts as a Loud event of the Route (S-198); the removal of an acknowledgement, never in a snoozed Alert Group (S-199); new Alerts in an attached Alert Group change no status (S-203) |
| C-09.FR-9 | S-111 (part), S-169, S-198 (part), S-199 (part), S-203 (part) | complete | chains that start over on a rise to critical (S-169); `rise_to_urgent` as a Loud event (S-198) and the order against new Alerts (S-199); a rise changes no status of an attached Alert Group (S-203) |
| C-09.FR-11 | S-118 (part), S-120 (part), S-123 (part), S-159 | complete | the Assignees and running chains on the page (S-159) |
| C-09.FR-13 | S-118 (part), S-120 (part), S-168 (part), S-159, S-206 (part), S-207 (part) | complete | the Assignee filter: the API (S-168) and the list (S-159); the filter Attached, the count and the mark: the API (S-206) and the list (S-207) |
| C-09.FR-14 | S-120 (part), S-123 (part), S-159, S-205 (part), S-206 (part), S-207 (part) | complete | the Teams in the header (S-120), the entries of each Destination (S-123), the running chains, Assignees and escalation entries (S-159); the notice, the section of attached Alert Groups and their Alerts on the page (S-206 for the API, S-207) and the Root message's line (S-205) |
| C-09.FR-15 | S-174 (part), S-175 (part), S-206 | complete | By Team and the filters of every view: the API (S-174) and the page (S-175); an Alert Group resolved with its main one is left out (S-206) |
| C-09.FR-16 | S-169 (part) | complete | the steps of the runs of Escalation chains are details, kept for `retention.alert_details`, pruned with them and hidden from "How it went" once removed (S-169) |
| C-09.FR-22 | S-118 (part), S-121 (part), S-148 (part), S-169 (part), S-155 (part), S-168 (part), S-156 (part), S-198 (part), S-199 (part), S-202 (part), S-203 (part), S-205 (part) | complete | the events of C-25 and C-26 (S-118, S-121) and of C-30 and C-32 (S-148, S-169, S-155, S-168, S-156); the loudness a Route's Loud events give the rows (S-198) and the row `alerts_added` (removes) (S-199); the five events of Attach and Detach (S-202, S-203) and their messenger column (S-205) |
| C-09.FR-27 | S-174 | complete | added for L2: who made the first acknowledgement, with the backfill from the Timeline |
| C-09.FR-28 | S-174 (part), S-175 (part) | complete | added for L2: By people in the API (S-174) and on the page (S-175) |
| C-09.FR-29 | S-174 (part), S-175 (part) | complete | added for L2: By Team in the API (S-174), the view and its link to By people (S-175) |
| C-09.FR-30 | S-174 (part), S-175 (part) | complete | added for L2: the filters in the API (S-174), on the page and in the URL (S-175) |
| C-09.FR-31 | S-176, S-227 (part) | complete | added for L2: the section on statistics (S-176) and its subsection on the dashboard (S-227) |
| C-09 UI | S-175 | complete | the views By Team and By people and the filters |
| C-09 API surface | S-174, S-223 (part), S-224 (part) | complete | `group_by=team`, the filters, `alert-group-actor-statistics` (S-174); `group_by=all` with the critical split, `alert-title-statistics` (S-223) and `page-statistics` (S-224) for the dashboard of C-40 |
| C-09.AC-8 | S-111 | complete |  |
| C-09.AC-27 | S-174 (part), S-175 (part) | complete | added for L2 |
| C-09.AC-28 | S-174 (part), S-175 (part) | complete | added for L2 |
| C-09.AC-29 | S-174 | complete | added for L2 |
| C-09.AC-30 | S-174 | complete | added for L2 |
| C-09.AC-31 | S-174 (part), S-175 (part) | complete | added for L2 |
| C-09.AC-32 | S-174 (part), S-175 (part) | complete | added for L2 |
| C-09.AC-33 | S-174 (part), S-175 (part) | complete | added for L2 |
| C-10.FR-1 | S-173 (part), S-169 (part), S-168 (part), S-202 (part), S-203 (part) | complete | an Acknowledge through a gateway only while nobody owns the Alert Group (S-173); chains stop (S-169); Assignees clear (S-168); the Commands Attach and Detach (S-202) and the refusal of the others on an attached Alert Group (S-203) |
| C-10.FR-2 | S-202 | complete | `alert-groups:attach` for Admins and Responders, a Service account included |
| C-10.FR-3 | S-173 | complete | the Transport `gateway` |
| C-10.FR-14 | S-206 | complete | the bulk Attach, and the bulk refusal of an attached Alert Group |
| C-10.FR-16 | S-202 | complete | `attach` and `detach` in `allowed_commands` |
| C-10.AC-23 | S-162 (part), S-113 (part) | complete |  |
| C-11.FR-2 | S-148 (part), S-155 (part) | complete | calls have a sender of their own (S-148); Pages are Thread replies of delivery (S-155) |
| C-11.FR-3 | S-111 | complete |  |
| C-11.FR-6 | S-121 (part), S-140 (part), S-169 (part) | complete | Storms with Team Destinations (S-121); `storm_started` (S-140); the chains of an Alert Group that a summary stood for start at its own Publication (S-169) |
| C-11.FR-14 | S-170 | complete |  |
| C-11.FR-20 | S-118 (part), S-121 (part), S-140 (part), S-155 (part), S-168 (part) | complete | the L2 lifecycle events of C-25 and C-26 (S-118, S-121); the Mentions of the entries (S-140); `paged` as Thread replies (S-155) and `assignees_changed` as a Root message update (S-168) |
| C-11.FR-21 | S-165 | complete | the delivery event "Mention not delivered" |
| C-12.FR-1 | S-125 (part), S-126 (part) | complete |  |
| C-12.FR-2 | S-125 | complete |  |
| C-12.FR-3 | S-125 (part), S-143 (part), S-148 (part), S-173 (part), S-155 (part), S-168 (part) | complete | the layout (S-125), undelivered Mentions (S-143), calls (S-148), their answers (S-173), Pages (S-155), Assignees (S-168) |
| C-12.FR-5 | S-215 | complete | revised for L2: a preview of a Root message takes an unsaved message layout and a Destination and returns the messenger payload, the number of Slack blocks and the blocks shortening cut |
| C-12.FR-8 | S-140 (part), S-143 (part) | complete | the Mention entries in delivery and on the form |
| C-12.FR-11 | S-125, S-171 (part) | complete | the shortening of the compact layout (S-125); the Mention line of a Bridge message within half of the body limit (S-171) |
| C-12.FR-12 | S-140 (part), S-165 (part), S-168 (part) | complete | an unlinked person left out and recorded (S-140, S-165); the Assignees named like the footer's people (S-168) |
| C-12 UI | S-129 | complete | "Labels in the message" and the preview of the layout |
| C-13.FR-3 | S-125 | complete |  |
| C-13.FR-8 | S-140 | complete | Mattermost groups for group entries and Team mentions |
| C-13.FR-9 | S-114 (part), S-143 (part) | complete | the button set (S-114) and the Mention entries (S-143); the Route and Team forms with Severity conditions are S-123's, under C-26.FR-1 |
| C-14.FR-16 | S-125 | complete |  |
| C-15.FR-2 | S-118 (part), S-121 (part), S-148 (part), S-169 (part), S-155 (part), S-177 (part), S-168 (part), S-156 (part), S-198 (part), S-206 (part) | complete | the L2 events in events mode (S-118, S-121, S-148, S-169, S-155), the people of `paged` (S-177), the `webhook_events` CHECK extended by S-118, S-121 and S-148; the Assignees in `alert_group` (S-168); `escalation_webhook` only to its own Destination (S-156); `notify` from the loudness a Route gives the event (S-198); the five events of Attach and Detach and `attached_to` in `alert_group` (S-206) |
| C-15.FR-11 | S-140 (part), S-177 | complete | Teams and the On-call as targets (S-140); the people that `paged` names (S-177) |
| C-16.FR-1 | S-216 (part), S-218 (part) | complete | revised for L2: a test message with an unsaved message layout, built from any Alert Group the preview shows (S-216), sent from the editor (S-218) |
| C-16.FR-4 | S-113 (part), S-125 (part) | complete |  |
| C-17.FR-1 | S-169, S-199 (part), S-204 (part) | complete | the first chain stops the ack timeout (S-169); the ack timeout and chains start over when new Alerts remove an acknowledgement (S-199); they stop on Attach and start over on Detach (S-204) |
| C-17.FR-2 | S-140, S-198 (part) | complete | the Mentions of the entries for `ack_timeout` (S-140); the notice Quiet where the Route does not list `ack_timeout` (S-198) |
| C-17.FR-3 | S-169 | complete | Unclaimed once every chain ran out |
| C-17.FR-11 | S-169, S-198 (part) | complete | `unclaimed` when the last chain runs out (S-169); `ack_timeout` and `notices_missed` take the loudness of the Route's Loud events (S-198) |
| C-18.FR-4 | S-155 (part), S-177 (part) | complete | a Page by Mention reaches a person only through an Account link: in Mattermost and Slack (S-155), in Telegram and through Bridges (S-177) |
| C-18.FR-6 | S-167 (part), S-150 (part) | complete | gateway contacts set only by their person, seen and removed by Admins (S-167, S-150) |
| C-18.FR-10 | S-145 | complete | gateway-only Connections not offered to link an account through |
| C-20.FR-1 | S-111 (part), S-112 (part), S-113 (part), S-114 (part), S-115 (part), S-118 (part), S-119 (part), S-120 (part), S-152 (part), S-153 (part) | complete | the default Notification rules (S-152, S-153) |
| C-20.FR-9 | S-115 (part), S-119 (part) | complete |  |
| C-22.FR-2 | S-139 (part), S-145 (part) | complete | group mentions and mentions of everyone in `info` (S-139); the roles (S-145) |
| C-22.FR-3 | S-140 (part), S-145 (part) | complete | the Mention entries of a Bridge Destination (S-140); no Destination for a gateway-only Connection (S-145) |
| C-22.FR-4 | S-126 | complete |  |
| C-22.FR-11 | S-145 (part), S-173 (part) | complete | the event `notification_acknowledged` read (S-145) and handled once per id (S-173) |
| C-22.FR-16 | S-171 (part), S-165 (part) | complete | group and everyone entries in `mentions` (S-171); the record of a linked person named by the plain name (S-165) |
| C-22.FR-21 | S-126 (part), S-145 (part) | complete | the check accepts `layout` (S-126) and checks the role `gateway` (S-145) |
| C-23.FR-3 | S-167 | complete | the Request log masks gateway contacts |
| C-24.FR-1 | S-139 | complete | the optional scope `usergroups:read` |
| C-24.FR-2 | S-139 | complete | the step `scopes` reports it and the Connection keeps it |
| C-24.FR-4 | S-125 | complete |  |
| C-24.FR-6 | S-171 | complete | `<!subteam^ID>`; no `<!everyone>` (F-127) |
| reference.md Loud and Quiet | S-198 (part), S-199 (part), S-203 (part), S-205 (part) | complete | the Loud events of a Route and the rows that stay Loud (S-198), the removal of an acknowledgement (S-199), every event of an attached Alert Group Quiet (S-203, S-205) |
| reference.md Buttons and links by status | S-205 | complete | an attached Root message has none |
