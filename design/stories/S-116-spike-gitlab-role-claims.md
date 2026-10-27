---
id: S-116
title: "Spike: GitLab's role claims — which token carries them, inherited memberships and discovery"
capability: C-25
kind: docs
layer: L2
depends_on: []
covers: [C-25.FR-4, C-03.FR-5]
files_touched:
  - design/facts.md
  - design/stories/S-117-claims-of-mapping-entries-subtrees-and-membership-from-the-identity-provider.md
acceptance:
  - "[C-25.FR-4, C-03.FR-5] `design/facts.md` has a new entry F-NNN that says, for GitLab with the scopes `openid`, `profile` and `email`, whether the claims `https://gitlab.org/claims/groups/owner`, `…/maintainer` and `…/developer` are in the ID token, in userinfo or in both, with the GitLab version and edition."
  - "[C-25.FR-4] `design/facts.md` has a new entry F-NNN that says whether those claims list only the groups a person is a direct member of with that role, or also the subgroups they inherit it in, and whether a person who is an Owner of a group appears in `…/maintainer` or `…/developer` of it too."
  - "[C-25.FR-4] `design/facts.md` has a new entry F-NNN that says whether discovery's `claims_supported` lists the three claims, and whether `groups` and `groups_direct` still behave as `docs/sign-in/oidc-gitlab.md` describes."
  - "[C-25.FR-4, C-03.FR-5] The item \"GitLab role claims\" leaves Pending in `design/facts.md`, and S-117's contract cites the new entries: its fake OIDC server's GitLab mode puts the claims where the facts say, and any assumption the facts contradict is corrected in it, or proposed as a change of C-03.FR-5 or C-25.FR-4 in the pull request."
verify: "make lint"
operator_attention: true
issue: null
---

# S-116. Spike: GitLab's role claims — which token carries them, inherited memberships and discovery

## Scope

**IN**

- The checks of L2.md §5.1 and of Pending in facts.md on GitLab's role claims (issue #369), on the maintainer's test
  GitLab, recorded as facts.
- The corrections of S-117's contract that the facts call for.

**OUT**

- The claim of every mapping entry, subtrees and membership from the identity provider: S-117.
- Mattermost groups and Slack user groups: S-138. Telegram: S-132. Push of Mentions: S-160.

## Contracts

- **Facts** (`design/facts.md`): new entries under a new section "GitLab" (or "OIDC identity providers"), numbered
  after the highest `F-NNN` when the spike merges, each with the date, the method and the GitLab version, and "Used in
  C-03.FR-5, C-25.FR-4"; the item "GitLab role claims" leaves Pending.
- **Story contract** (`S-117`): the GitLab mode of the fake OIDC server and the claims it reads follow the facts; if
  the facts contradict C-03.FR-5 ("a claim is read from the ID token, then from userinfo") or C-25.FR-4, the pull
  request proposes the change of the PRD for the maintainer instead of editing it.

## Steps

1. The maintainer runs the checks of Verification on the test GitLab, with scripts an agent may prepare outside the
   repository. Check: the observations of each step are written down with the raw JSON of the tokens, secrets removed.
2. Write the facts and move the item out of Pending. Check: `make lint`.
3. Correct S-117's contract where the facts differ from it. Check: `scripts/stories-sync --dry-run` parses the file.

## Verification

On the maintainer's test GitLab (self-managed; record the version and edition, and repeat step 4 on `gitlab.com` if a
test account there is at hand):

1. Read `https://<gitlab>/.well-known/openid-configuration`. Record `claims_supported` and `scopes_supported`; expected
   to learn whether the three role claims are listed.
2. Create the groups `muster-spike/platform` and `muster-spike/platform/infra` and three test users: `alice`, a direct
   Owner of `muster-spike/platform` (and so an inherited Owner of `…/infra`); `bob`, a direct Maintainer of
   `muster-spike/platform/infra`; `carol`, a direct Developer of `muster-spike/platform`.
3. Create an OAuth application with the scopes `openid`, `profile` and `email` and the redirect URI of a local script
   that runs the authorization code flow with PKCE.
4. Sign in as each user; decode the ID token's payload and record which of `groups`, `groups_direct`,
   `https://gitlab.org/claims/groups/owner`, `…/maintainer` and `…/developer` it carries and with which values; call
   the userinfo endpoint with the access token and record the same.
5. Compare: does `alice`'s `…/owner` list `muster-spike/platform/infra` too (inherited), and does it appear under
   `…/maintainer` or `…/developer`? Does `bob`'s list name only `…/infra`?
6. Optionally, repeat step 4 with the scope `read_user` added, and record whether anything changes.

The record of each step goes into the facts; the raw tokens never go into the repository.

## Open questions

None; the spike answers them.

## Notes

- Suggested commit: `docs: record which tokens carry gitlab's role claims`.
- The maintainer performs the checks (operator attention): they need the test GitLab, its admin account and three test
  users. An agent may write the script of step 3 outside the repository and run it on the maintainer's instructions.
- `covers` names C-25.FR-4 and C-03.FR-5 in part: the spike settles which token carries the claims that their entries
  read, and S-117 implements both.
- No example in the repository names a real company or person; the groups are `muster-spike/…` and the users `alice`,
  `bob` and `carol`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-4 | partial | the facts of GitLab's role claims; the requirement is S-117 |
| C-03.FR-5 | partial | revised for L2: which token carries the claims an entry reads; the requirement is S-117 |
