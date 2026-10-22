# Scanning with the iPhone

The iPhone scans with the apps it already has, and a Shortcut sends the scan
to your server. Nothing is installed on the server for this, and there is no
camera scanning in the web UI. The Shortcut is a few actions you build once
(below); it uploads each file with the same upload the web UI uses, with an
API key that can do nothing else.

::: warning The server must be reachable from the phone
The phone needs the server's address over HTTPS: at home through
`tailscale serve` or a reverse proxy, away from home as in [Running on the
internet](./internet). Over plain HTTP the key and the scans cross the
network in the clear.
:::

::: info What is checked, and what is not
The server side below is checked against the code. How the Shortcuts app
behaves is not: this page was written without a device, and every statement
about iOS that may differ on your version is marked **verify on the
device**.
:::

## Scan a document

Both apps scan with edge detection, perspective correction and several pages
in one go, and give a PDF:

- **Files:** Browse, then the **⋯** menu at the top right, **Scan
  Documents**. Save the PDF to a folder, or share it straight from the scan
  screen's result.
- **Notes:** in a note, the camera button, **Scan Documents**. After saving,
  tap the scan, then the share button.

Then use **Share** and pick the Shortcut ([below](#build-the-shortcut)).
Photos of documents from the Photos app work too: the server accepts PDF,
JPEG, PNG and HEIC. Files and Notes name every scan "Scanned Document" or
similar, so several scans share a title until you change them; see [Titles
of scans](#titles-of-scans).

## Create an API key

Do this in Safari on the phone, or on any computer.

1. Log in to the web UI and open **Profile**, then **API keys**.
2. Name the key after the device, for example `iPhone`. The name appears in
   the [audit log](./audit-log) next to every upload made with the key.
3. Tick **upload: add documents (for a phone or scanner)** and nothing else.
4. Set an expiry; 365 days is a sensible choice for a phone.
5. Confirm with your password (and an authentication code if you use
   two-factor login), then **Create API key**.
6. Copy the key (`pl_…`). It is shown once; the server stores only a hash.

A key with `upload` can add documents to your account and nothing else: it
cannot list, open or download any document, and cannot change or delete
anything. A key with `write` would also change and delete documents, tags,
correspondents and document types, so do not use it here. See [API
keys](../reference/security#api-keys).

## Build the Shortcut

::: tip A ready-made Shortcut
_A link to a Shortcut that you can add with one tap is not yet available;
the maintainer adds it here once it exists. Until then, build the Shortcut
from these steps; they are the reference, and the link is only a
convenience._
:::

Open the Shortcuts app, tap **+**, and name the Shortcut `Send to
paperless-go`. Add the actions in this order. Action names are those of
current iOS in English; yours may be labelled slightly differently.

1. **Shortcut settings** (the **ⓘ** button): turn on **Show in Share Sheet**
   and, under **Share Sheet Types**, keep only **PDFs** and **Images**.
   _Verify on the device:_ the menu names and where the types are chosen.
2. **Text** with your server's address, for example
   `https://docs.example.com`, with no slash at the end. The next steps call
   it the _server_ text.
3. **Text** with the API key you copied (`pl_…`): the _key_ text.
4. **Repeat with Each** over the **Shortcut Input**. The following steps go
   inside the loop, and **Repeat Item** is the file being sent.
5. **Get Name** of the **Repeat Item**. _Verify on the device:_ whether the
   name includes the extension (`Scan.pdf`) or not. Either works: the type
   comes from the file's content, and the server removes an extension when
   it makes the [title](#titles-of-scans).
6. **URL Encode** of that name (as input choose the name from step 5). The
   server decodes the name with percent-decoding, so spaces must become
   `%20`. _Verify on the device:_ with a name such as `Rechnung März
   2026`, the encoded text must show `%20` for the spaces and `%C3%A4` for
   `ä`. If spaces come out as `+`, the title on the server would contain
   a `+`; then replace `+` with `%20` using a **Replace Text** action.
7. **Get Contents of URL**:
   - URL: the _server_ text followed by `/api/documents` (type the path
     after the variable, or join them in a **Text** action).
   - Tap **Show More**. Method: **POST**.
   - Headers: `Authorization` with the value `Bearer ` (with a space)
     followed by the _key_ text, and `X-File-Name` with the encoded name
     from step 6.
   - Request Body: **File**, and as the file the **Repeat Item**.

   _Verify on the device:_ that the **File** request body sends the file
   itself as the body, not as a form (`multipart/form-data`). The server
   accepts only the raw file; a form answers `415`.
8. **Get Dictionary Value** for the key `duplicate` of the result of step 7.
   Do not read `title` or `existing` from the answer to a duplicate: a key
   with `upload` gets neither (see [Answers](#answers-of-the-server)).
9. **If** the `duplicate` value **is** `true` (or **has any value**, if your
   Shortcuts version offers only that):
   - **Show Notification**: `Already in your archive`.

   **Otherwise:**
   - **Get Dictionary Value** for `title` of the result of step 7, then
     **Show Notification**: `Uploaded: ` followed by that title.

   _Verify on the device:_ that the answer to a duplicate, `200` with
   `{"duplicate": true}`, can be read with **Get Dictionary Value**, and
   how the **If** compares the value (a true value, the text `true`).
10. **End If**, then **End Repeat**.
11. After the loop, optionally **Show Notification** with the number of
    files (**Count** of the **Shortcut Input**).

Tap **Done**. Edit the _server_ and _key_ texts when the address or the key
changes; that is the only place they are stored.

### Titles of scans

The server makes the title from the file name without its extension, and
`upload-YYYYMMDD-HHMMSS` when no name is sent. So a scan from Notes usually becomes
"Scanned Document" (from Files, the name of the file you saved; _verify on
the device_). The scan
lands in your account as an inbox document, text recognition runs as for any
upload, and you change the title in the web UI later.

To be asked for a title on every scan instead, add before step 5 an **Ask
for Input** (type Text, prompt `Title`), and use its result in place of the
**Get Name** result in step 6. Everything after stays the same. This is
optional and costs a tap per scan.

## Use it

Scan in Files or Notes, tap **Share**, scroll to **Send to paperless-go**.
The first run may ask whether the Shortcut may send data to your server's
domain: allow it (_verify on the device_). Several files shared at once go one request each; the
Shortcut loops over them.

_Verify on the device:_ that the Shortcut shows up in the share sheet for
PDFs from Files and Notes and for images from Photos.

The Shortcuts app also offers actions that scan documents on their own;
_verify on the device_ whether your iOS has one. If it does, a variant that
scans and uploads from an icon on the Home Screen, without Files or Notes,
uses it in place of the **Shortcut Input** in step 4: pass the scanned files
to the same loop.

## Answers of the server

The upload is `POST /api/documents` with the file as the request body and
the headers `Authorization: Bearer pl_…` and `X-File-Name` (percent-encoded;
without it the title is `upload-YYYYMMDD-HHMMSS`). The type is detected from
the content; the content type header does not matter for a key.

| Status | Meaning | What to do |
|---|---|---|
| `201` | Stored. The answer has the document, including `id` and `title`. | Nothing. |
| `200` | The file is already in your archive (same content). The answer to a key without `read` is only `{"duplicate": true}`, without which document. | Nothing; nothing was added. |
| `400` | The `X-File-Name` header is not percent-encoded, or the upload was interrupted. | Check step 6; share again. |
| `401` | The key is wrong, revoked or expired. | Create a new key and edit the _key_ text. The Log page of an admin tells which. |
| `403` | The key lacks the `upload` scope, or its owner must first choose a password or set up two-factor login. | Use a key with `upload`; log in once in the browser. |
| `413` | Larger than the upload limit (100 MiB by default, `PAPERLESS_GO_MAX_UPLOAD_SIZE`). | Scan fewer pages per file. |
| `415` | Not a PDF, JPEG, PNG or HEIC file, or sent as a form. | Check the step 7 body. |
| `422` | The file cannot be read, or an image has more than 50 megapixels. | Scan again. |
| `429` | More than 600 requests in a minute with this key; the `Retry-After` header says how long to wait. | Wait. |

Errors come as JSON with an `error` text. _Verify on the device:_ what
**Get Contents of URL** does on these statuses. It may stop the Shortcut
with its own alert that shows only the status, not the body; then the table
tells what the status means. A Shortcut that carries on gives the `error`
text as a dictionary value.

## What Shortcuts cannot do

- **No safe place for the key.** The key is text inside the Shortcut;
  anybody who can unlock the phone and open Shortcuts can read it. That is
  why the key has `upload` only, its own name and an expiry, and why it is
  revoked with one tap.
- **No background upload and no retry.** The Shortcut runs while you wait;
  if an upload fails, share the file again.
- **No tags on the phone.** The scan arrives in the inbox; sorting happens
  in the web UI.

## When the phone is lost

Revoke its key, and nothing else needs to change: the key reaches no login
and no documents.

- In the web UI: **Profile**, **API keys**, **Revoke** next to the key; an
  admin can also revoke a user's key on the **Users** page.
- On the server: `./pgo user revoke-api-keys NAME` revokes all keys of a
  user.

The list shows when each key was last used. The audit log records every
upload with the key's name, so you see what was added with it:

```sh
./pgo audit export --since 2026-10-01 --user NAME --action documents --format jsonl
```

On the Log page the entry reads "Uploaded document" with API key
`iPhone`. See [Audit log](./audit-log).

## Limits

- 100 MiB per file by default (`PAPERLESS_GO_MAX_UPLOAD_SIZE`), and 50
  megapixels for an image. A reverse proxy needs a body limit of at least
  that ([Running on the internet](./internet#reverse-proxy)).
- One request per file; there is no batch upload.
- A slow mobile line is fine: an upload may take up to an hour.
- A duplicate is found by the file's content (SHA-256), per user.
