# Changelog

## 0.1.0 (2026-10-09)


### Features

* **auth:** add API server with local sign-in, sessions and the bootstrap admin ([#108](https://github.com/muster-io/muster/issues/108)) ([de3e476](https://github.com/muster-io/muster/commit/de3e4764d33d54d1ae9367b418f5d26e17a164f0))
* **auth:** add oidc account linking, conversion to local and background re-checks ([#117](https://github.com/muster-io/muster/issues/117)) ([f5814cc](https://github.com/muster-io/muster/commit/f5814ccd8b45515a9f90fa655021128e9fa58c5b))
* **auth:** add oidc settings, proxy settings and oidc sign-in ([#113](https://github.com/muster-io/muster/issues/113)) ([1306b61](https://github.com/muster-io/muster/commit/1306b61e2ff6eb14f10e6e2177fffe3b130ccfe5))
* **auth:** add totp, the totp policy, system notices and live updates ([#112](https://github.com/muster-io/muster/issues/112)) ([56b87be](https://github.com/muster-io/muster/commit/56b87be8f47afb2dda69a1063afe774284854d15))
* **delivery:** add error classes, broken destinations and recovery, storms and republication ([#149](https://github.com/muster-io/muster/issues/149)) ([c711902](https://github.com/muster-io/muster/commit/c7119022a01835d8282fd08eca563482939e9b79))
* **delivery:** reconcile root messages with limiters, an interactive path and batched threads ([#148](https://github.com/muster-io/muster/issues/148)) ([36ab2df](https://github.com/muster-io/muster/commit/36ab2df24f08794e51abc6d4d3333aec32f02183))
* **delivery:** send the alert group hint when a delivery ends ([#172](https://github.com/muster-io/muster/issues/172)) ([2a6cc06](https://github.com/muster-io/muster/commit/2a6cc0698d71aa1011f5bb611d30aa1d24e9797f))
* **destinations:** add destination tests and previews for every type ([#190](https://github.com/muster-io/muster/issues/190)) ([a94a116](https://github.com/muster-io/muster/commit/a94a11628ae21dccaae3d79b6ba1355cdf4a4bd3))
* **groups:** add commands, takeover, refusals and bulk commands ([#145](https://github.com/muster-io/muster/issues/145)) ([cc1b2b2](https://github.com/muster-io/muster/commit/cc1b2b2e19e3898a5e1761308e6c2f9795feae99))
* **groups:** add notes, snooze ends, owner filters and the release of disabled owners ([#146](https://github.com/muster-io/muster/issues/146)) ([9cb3a6d](https://github.com/muster-io/muster/commit/9cb3a6d26e8192f6397c1866cc6f1ffbc6c49b66))
* **groups:** add the alert group lifecycle, the timeline and lifecycle events ([#140](https://github.com/muster-io/muster/issues/140)) ([71d88a3](https://github.com/muster-io/muster/commit/71d88a3d2059055ac79a91a162852a21b64002b6))
* **groups:** add the alert group list, search, counts, statistics, retention and live hints ([#142](https://github.com/muster-io/muster/issues/142)) ([89856ef](https://github.com/muster-io/muster/commit/89856ef4b4ceecbd0559a35024765e85ad8db14e))
* **heartbeat:** add the heartbeat endpoint, leader checks and the stale scan ([#130](https://github.com/muster-io/muster/issues/130)) ([dcdd7d7](https://github.com/muster-io/muster/commit/dcdd7d7e48dba3840f9cfc962d3d01a910780b9a))
* **ingest:** add integrations, integration tokens and the ingestion endpoint ([#122](https://github.com/muster-io/muster/issues/122)) ([52588ee](https://github.com/muster-io/muster/commit/52588ee3ac3b038720da75c4a0f75effd8478014))
* **ingest:** add internal alerts, the built-in integration, replay and learned routes ([#128](https://github.com/muster-io/muster/issues/128)) ([7825756](https://github.com/muster-io/muster/commit/78257561e3b518cf418d603327175cc204f3692d))
* **ingest:** process snapshots into alerts with gone, truncation and continuation semantics ([#126](https://github.com/muster-io/muster/issues/126)) ([f141a36](https://github.com/muster-io/muster/commit/f141a3671c5499ee9264a17a7301b88603eccfd8))
* **keyring:** add master key keyring, key canary and organization defaults ([#105](https://github.com/muster-io/muster/issues/105)) ([680496a](https://github.com/muster-io/muster/commit/680496a5a524a24e61c7ac51c670ef23a24a4774))
* **leader:** prune ended sessions and stale sign-in throttles ([#109](https://github.com/muster-io/muster/issues/109)) ([02ca570](https://github.com/muster-io/muster/commit/02ca570696ef6af264ba856d41c4bee66bdaf55f))
* **links:** add link kinds and name the rules of an in_use table ([#158](https://github.com/muster-io/muster/issues/158)) ([a479f55](https://github.com/muster-io/muster/commit/a479f559da1e05a6b2fd7416da86055edc92c776))
* **links:** add lookup tables, link rules and mention settings ([#154](https://github.com/muster-io/muster/issues/154)) ([4ab9609](https://github.com/muster-io/muster/commit/4ab9609b0a3f49862fa125e327fe8073e06c0a17))
* **logging:** add domain logger with log event and metric registries ([#99](https://github.com/muster-io/muster/issues/99)) ([3f1d72f](https://github.com/muster-io/muster/commit/3f1d72fbdaa41f9fb9df45cab34ad36eb54a4988))
* **mattermost:** add mattermost connections, destinations and the destination check ([#159](https://github.com/muster-io/muster/issues/159)) ([94c5023](https://github.com/muster-io/muster/commit/94c50234e650a4e04309d60dd5d03ac4aa312e32))
* **mattermost:** add the mattermost adapter, button callbacks and the delivery problem filter ([#161](https://github.com/muster-io/muster/issues/161)) ([7818264](https://github.com/muster-io/muster/commit/78182643e636fe71ea48aac48cea0ed2aba92b06))
* **messages:** add the template sandbox, the default message, built-in texts and previews ([#152](https://github.com/muster-io/muster/issues/152)) ([7923d45](https://github.com/muster-io/muster/commit/7923d45d3f993bafb4fea301e7daa0af4a7f389f))
* **outbound:** add outbound HTTP package with address policy and proxies ([#107](https://github.com/muster-io/muster/issues/107)) ([61063d4](https://github.com/muster-io/muster/commit/61063d475180f71c496f3a3c105ec586487ad469))
* **routing:** add routes, matchers, evaluation order, severity levels and route profiles ([#133](https://github.com/muster-io/muster/issues/133)) ([150f489](https://github.com/muster-io/muster/commit/150f489aae74275a863d2f5a65eebb1d2ba5b67a))
* **routing:** add the group key preview and route suggestions ([#134](https://github.com/muster-io/muster/issues/134)) ([5c3d7bc](https://github.com/muster-io/muster/commit/5c3d7bc70c3e63fec9f7fef057c8f914b74fe12a))
* **runtime:** add bootstrap settings, database connections, migrations and listeners ([#102](https://github.com/muster-io/muster/issues/102)) ([93ab7f5](https://github.com/muster-io/muster/commit/93ab7f59b24bb913c0b8a8f0567b6c1d11156e0a))
* **runtime:** add leader election, partition maintenance, downtime recovery and doctor ([#106](https://github.com/muster-io/muster/issues/106)) ([b04c8cb](https://github.com/muster-io/muster/commit/b04c8cbb47d1bd3f1d10bafac9a5ffcc1f336c3f))
* **telegram:** add button presses with callback answers and the update router lock ([#183](https://github.com/muster-io/muster/issues/183)) ([6bd1537](https://github.com/muster-io/muster/commit/6bd1537df2696a5dbfe2667f94949803ed8ce641))
* **telegram:** add comment threads with the copy buffer and unattached replies ([#182](https://github.com/muster-io/muster/issues/182)) ([c1f9c06](https://github.com/muster-io/muster/commit/c1f9c06370ceab7f0288484c22864052067c8e98))
* **telegram:** add telegram connections with the dry probe, long polling and webhooks ([#173](https://github.com/muster-io/muster/issues/173)) ([4709c2e](https://github.com/muster-io/muster/commit/4709c2e13f6c7a42e07d8ca2dce24e9612125a98))
* **telegram:** add telegram destinations, the destination check and root message posts ([#181](https://github.com/muster-io/muster/issues/181)) ([60b5263](https://github.com/muster-io/muster/commit/60b5263effd249c5793baaf693467212f48fdaba))
* **tokens:** add personal access tokens, service accounts and token authentication ([#120](https://github.com/muster-io/muster/issues/120)) ([c224e3c](https://github.com/muster-io/muster/commit/c224e3cb930e66da72744cb135a37ce7bc59e5df))
* **users:** add user administration, password setup links and the audit log api ([#110](https://github.com/muster-io/muster/issues/110)) ([4560e0b](https://github.com/muster-io/muster/commit/4560e0b9c1a511379fafd9724c8fabd22fc25891))
* **web:** add application shell, sign-in, password setup, totp and profile pages ([#118](https://github.com/muster-io/muster/issues/118)) ([1bf122e](https://github.com/muster-io/muster/commit/1bf122e0e718d90a703204425b995415839b06e1))
* **web:** add command buttons, dialogs, the note box and bulk selection ([#147](https://github.com/muster-io/muster/issues/147)) ([941ea42](https://github.com/muster-io/muster/commit/941ea42c42f24e31de783502a04337d36d081c9a))
* **web:** add destination pages, route delivery settings, delivery state and the delivery problem filter ([#170](https://github.com/muster-io/muster/issues/170)) ([a80d05e](https://github.com/muster-io/muster/commit/a80d05e0feac69fedc533bffb3f5482008dacc49))
* **web:** add heartbeat settings, state badges and banners ([#132](https://github.com/muster-io/muster/issues/132)) ([279d3b4](https://github.com/muster-io/muster/commit/279d3b4d3f732b3fc575978acd7c232728b3f052))
* **web:** add integration pages, the token dialog and the stored snapshot viewer ([#123](https://github.com/muster-io/muster/issues/123)) ([da66e77](https://github.com/muster-io/muster/commit/da66e77977014a6a9ff83be3e1785002cc9b25fe))
* **web:** add learned alertmanager routes, warnings and the alerts view ([#129](https://github.com/muster-io/muster/issues/129)) ([878605b](https://github.com/muster-io/muster/commit/878605bf6880e3b76e32700230638920fe6c3f01))
* **web:** add personal access token and service account pages ([#121](https://github.com/muster-io/muster/issues/121)) ([ab1fb74](https://github.com/muster-io/muster/commit/ab1fb7466b4331739c8887ffe8f86d793fa18442))
* **web:** add route message editors, lookup tables and link rules pages ([#156](https://github.com/muster-io/muster/issues/156)) ([c924817](https://github.com/muster-io/muster/commit/c924817c12fcb4ab5002bd24ef88c76befdb7d3d))
* **web:** add telegram connection pages and the telegram destination form ([#184](https://github.com/muster-io/muster/issues/184)) ([b523893](https://github.com/muster-io/muster/commit/b523893d0a0aca02b35daa898ba84b0fa16e0738))
* **web:** add the alert group list and page, usable at phone width ([#143](https://github.com/muster-io/muster/issues/143)) ([8979f6f](https://github.com/muster-io/muster/commit/8979f6fb0dc314eb34a797f65881886ac2912bfe))
* **web:** add the mattermost connection pages ([#168](https://github.com/muster-io/muster/issues/168)) ([d303aa8](https://github.com/muster-io/muster/commit/d303aa81c9e598c26cfa379c8893e0973d6c3994))
* **web:** add the outgoing webhook destination form with secrets and signing secret actions ([#188](https://github.com/muster-io/muster/issues/188)) ([c8ae2dd](https://github.com/muster-io/muster/commit/c8ae2dd3bf9240ca1460c13993eff17c531b7731))
* **web:** add the routes list, route editor, matcher builder and group key preview ([#135](https://github.com/muster-io/muster/issues/135)) ([72093b1](https://github.com/muster-io/muster/commit/72093b10f26c27469367c3979435a39b524b5c80))
* **web:** add the statistics page, route lifecycle settings and delete dialog additions ([#144](https://github.com/muster-io/muster/issues/144)) ([8ea5677](https://github.com/muster-io/muster/commit/8ea56774015416c361af8ad78800ad8de3c3dc56))
* **web:** add users, oidc settings, security and audit log pages ([#119](https://github.com/muster-io/muster/issues/119)) ([a989e8d](https://github.com/muster-io/muster/commit/a989e8d33d6a282ed13d8d5ac26548bb861426c5))
* **webhooks:** add the outgoing webhook events mode with signing and secrets ([#175](https://github.com/muster-io/muster/issues/175)) ([da6dd9e](https://github.com/muster-io/muster/commit/da6dd9e54b791f9473f17dff67dbfa8f567e9377))
* **webhooks:** add the outgoing webhook template mode with extraction and threads ([#186](https://github.com/muster-io/muster/issues/186)) ([3b06982](https://github.com/muster-io/muster/commit/3b0698205e216432dcf600a126e5167a7710ed19))


### Bug Fixes

* **api:** round learned route intervals once and report matcher regexp errors as typed ([#131](https://github.com/muster-io/muster/issues/131)) ([e6cc135](https://github.com/muster-io/muster/commit/e6cc13538e68751a68cd3b056c9b68ab2af8269c))
* **deps:** update go modules (non-major) ([#87](https://github.com/muster-io/muster/issues/87)) ([9ff94e6](https://github.com/muster-io/muster/commit/9ff94e67a109e6f3c71e95498761351da6f7017f))
* **groups:** group firing alerts that have no alert group ([#141](https://github.com/muster-io/muster/issues/141)) ([22446ff](https://github.com/muster-io/muster/commit/22446ffc10fb02d898102d02bae04acc17c43bcc))
* **integrations:** tell users to keep a catch-all route after the muster route ([#127](https://github.com/muster-io/muster/issues/127)) ([d516550](https://github.com/muster-io/muster/commit/d51655096f3921a9c7339dce57937c0e35d3dccf))
* **internalalerts:** keep destination names current after a rename ([#187](https://github.com/muster-io/muster/issues/187)) ([8c10266](https://github.com/muster-io/muster/commit/8c102666b6a54834131d91952f3b331f8d18b61e))
* **links:** refuse renaming a lookup table in use and record the lookup limits ([#155](https://github.com/muster-io/muster/issues/155)) ([47c3d03](https://github.com/muster-io/muster/commit/47c3d039ba6c2a8236851008b8baf759c6b64391))
* **links:** yield no explore link without an environment or cluster label ([#162](https://github.com/muster-io/muster/issues/162)) ([4d70a81](https://github.com/muster-io/muster/commit/4d70a81d4c4782747e378895457f510c0c86c4b3))
* **mattermost:** fall back to ephemeral_text when a press answer cannot be posted ([#163](https://github.com/muster-io/muster/issues/163)) ([0630ba4](https://github.com/muster-io/muster/commit/0630ba4a82b82467cc353b65f3b13e6aceb09e64))
* **telegram:** reschedule edits during a press and cap concurrent updates ([#185](https://github.com/muster-io/muster/issues/185)) ([806eaa6](https://github.com/muster-io/muster/commit/806eaa6475c10dbf806426a6dd73d28c236f98c3))


### Performance Improvements

* **ingest:** process alertmanager groups of an integration in parallel ([#164](https://github.com/muster-io/muster/issues/164)) ([1dd0dc7](https://github.com/muster-io/muster/commit/1dd0dc7d9985c645ebc24ca7a4da2f1bef97f864))
* **web:** split the bundle by route and name every audited field ([#124](https://github.com/muster-io/muster/issues/124)) ([93be32d](https://github.com/muster-io/muster/commit/93be32de0d801fd2238e3aacc5f86f2e236e141e))

## Changelog

release-please maintains this file from the conventional-commit messages of merged pull requests.
