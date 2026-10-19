# Key rotation

Muster encrypts its Secrets and signs the buttons of its messages with the **master keys** in `MUSTER_SECRET_KEYS`. These
keys form the Keyring; one of them is **active** and encrypts and signs, and all of them can still decrypt and verify.
Which key is active is stored in the database, not in the environment. Muster never generates a master key and never
writes the environment: you add a new key yourself, then tell Muster to use it.

Rotate when a key may have leaked, when a person who knew it leaves, or on a schedule you keep. The rotation takes two
deliberate steps, so that a rolling restart never leaves a replica unable to read what another one wrote.

## Step 1: add the new key everywhere

1. Generate a key: `openssl rand -base64 32`.
2. Add it to `MUSTER_SECRET_KEYS`, **after** the current key, separated by a comma. With the Helm chart the value comes
   from the key `secret-keys` of your Secret (`masterKeysKey`); `MUSTER_SECRET_KEYS_FILE` takes one key per line.
3. Roll the change out to every replica. Muster applies a changed environment only on start, so restart the pods.

Both keys stay in the list. The key that is active does not change yet, and nothing is re-encrypted.

Each replica records the key ids it holds every 30 seconds, and counts as live while its record is younger than 2
minutes. Open **Organization → Keyring**. It lists every key by its id, which one is *Active*, and for each key *Held by*
the replicas that have it and *Missing on* those that do not. The new key has the state **Not active yet**, because it
has never been active. Muster never calls such a key removable.

## Step 2: activate it

Run the command with the same environment as the server (see [the CLI](cli.md#muster-secrets-rotate-key)), giving the
id of the new key as the Keyring page shows it:

```sh
muster secrets rotate-key --activate <id> --actor alice
```

`--actor` names who runs the command and goes to the Audit log. Muster refuses to activate a key while a live replica
does not hold it, names those replicas, and exits 1. Wait until the Keyring page shows no replica as *Missing on*, or
until the replica that lacks the key stops being live, and run the command again.

When every live replica holds the key, one transaction:

- makes the key active,
- re-encrypts every stored Secret and the key canary with it, and
- records `keyring.key_activated` in the Audit log.

The replicas switch to the key without a restart. Then the Leader recomputes the message of every open Alert Group whose
buttons the old key signed, so that the delivery worker edits each Root message and signs its buttons with the new key.
The edits go through the usual limits of the messengers, so a large backlog takes a while.

## Remove the old key

The old key is still needed while a Secret or an open Root message depends on it. The Keyring page shows each older
key as *Still needed*, with the counts, until nothing does; then it says **Key `<id>` is no longer used and can be
removed from MUSTER_SECRET_KEYS.** `muster doctor` prints the same on its `keyring` line. Only a key that was active
before is ever called removable.

Then remove the key from `MUSTER_SECRET_KEYS` and roll the change out. Removing the key before Muster says so breaks
what still depends on it; the start of a replica that lacks the *active* key fails with "master key does not match the
database".

Keep the old key in your records until the backups that need it are gone: a [backup](backup-and-restore.md) taken before
the rotation restores only with the key that was active then.

## What stops working when the old key goes

- **Buttons in Thread replies.** Muster re-signs only the buttons of Root messages. A Reminder, for example, is a Thread
  reply, and keeps the signature it was sent with. Once the old key is gone, a press on such a button is answered
  privately: "This button has expired; use the buttons on the Alert Group's message". Nothing changes in the Alert
  Group, and the Root message works.
- **Web pages that were open before the rotation.** The CSRF token that a web page sends with each change carries the id of
  the key that signed it, and verifies while Muster holds that key. A page that has been open since before the rotation
  has the old token. Once the old key is gone, the next change from that page is refused (`403`, `csrf_invalid`) and the
  page says "The page is out of date. Reload it and try again." Reload the page, and it works again with a token of the
  new key. Nobody has to sign in again.

Nothing else changes: Personal access tokens and Service account tokens are stored as hashes and are not affected, and the
Signing secret of an outgoing webhook Destination does not change (Muster only re-encrypts it).

## If something goes wrong

| Symptom | Cause | What to do |
|---|---|---|
| `Replicas {names} do not hold key {id} yet.` | a live replica has not restarted with the new key | finish the rollout, or wait until the record of a gone replica expires |
| `key {id} is not in the master keys of this process` | you ran the command without the new key in `MUSTER_SECRET_KEYS` | give the command the same environment as the server |
| a replica stops with "master key does not match the database" | it lacks the active key | add the key back to its environment |
| exit code 2 | `--activate` or `--actor` is missing | add the flag |
