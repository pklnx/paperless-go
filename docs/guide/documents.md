# Documents

The web UI opens on your documents: the upload panel at the top, the
search field, and the list below it. The sidebar on the left (behind the
menu button on phones) chooses what the list shows.

## The sidebar

| Entry | The list shows |
|---|---|
| All documents | Every document. Choosing it also clears the search. |
| Inbox | Documents still in the inbox: new documents arrive here. |
| Favorites | Documents marked as favourite. |
| A tag | Documents with the tag or one of its sub-tags. |
| A correspondent, a document type | Documents with it. |

Each entry shows how many documents it holds; a tag counts the documents of
its sub-tags too, each document once. Tags form a tree: the arrow next to a
tag folds its sub-tags away, and the browser remembers what you folded.
Names are sorted in the language of the UI.

One entry is active at a time. It shows as a chip above the list; the ×
on the chip removes it. To combine filters, type them into the search, for
example `tag:Steuer correspondent:Finanzamt` (see [Search](./search)).

Your log, your profile, the user management (admins) and logging out are
at the foot of the sidebar.

## The list

Each row shows the thumbnail of the first page, the title (the file name
when the title is empty), the document date, the correspondent, the type,
the tags in their colours, whether the document is in the inbox or a
favourite, and how its processing goes. A failed processing has a Retry
button. "Load more" at the end shows the next 50 documents.

The sort menu next to the heading sorts by the date added or by the
document's date; the arrow next to it switches between newest and oldest
first. A search lists the best matches first.

What the list shows is part of the page's address, so the browser's back
button returns to the previous list, and a bookmark or a reload opens the
same list again.

## A document

The title of a row opens the document: on a large screen as a page over
the list, on a phone as a sheet. The page's address names the document, so
Back closes it again and a reload or a bookmark opens it.

On the left is the document itself: the archive PDF (with the recognized
text) once processing made it, else the uploaded PDF. "Fit width", − and +
zoom; the counter shows the page you are on. The text of the pages can be
selected and found with the browser's search. A JPEG or PNG that is not
processed yet shows as an image; a HEIC photo shows once its PDF is ready.

On the right is the form: title, document date, archive serial number,
correspondent, document type, tags, notes, inbox and favourite. The
correspondent, type and tag fields search as you type; a name that does not
exist yet can be created right there ("Create …").

- **Save** stores the changes. Only the fields you changed are sent.
- **Done** saves, takes the document out of the inbox and, in the inbox,
  opens the next document. Elsewhere it closes the document.
- An archive serial number that another document has is refused; the
  message links to that document.
- If the document was changed elsewhere while you edited it (a change of
  several documents, another tab or another user), saving is refused: the
  document is loaded again, your changes are kept on top of the new state
  (tags you added or removed, on top of the new tags), and you check them
  and save again. Nothing is saved by itself.
- While a save is running, previous and next wait for it.
- The notes are shown formatted; **Edit** opens them as text, **Preview**
  shows the text as edited (empty notes open as text). They use the same
  Markdown as the app, so a note looks the same in both: headings (`#` to
  `###`), lists with `-`, `*`, `+` or `1.` (indent by two spaces or a tab
  per level), read-only tasks `- [ ]` and `- [x]`, quotes `>`, code
  between ```` ``` ```` or `~~~` lines, rules `---`, `**bold**`, `*italic*`,
  `~~struck~~`, `` `code` ``, and links `[text](https://…)`. A line break
  stays a line break. Addresses written without Markdown become links as
  in the app: `https://…`, `http://…` and `www.…` addresses, e-mail
  addresses (`name@example.com`) and `mailto:` and `tel:` addresses, but
  not inside `` `code` ``, a code block or a link's text. A link ends
  before punctuation that ends the sentence (`.`, `,`, `!`, `?`, a quote)
  and before a closing bracket it did not open, so `(https://a.example).`
  links `https://a.example`. Links open in a new tab and work only for
  `http`, `https`, `mailto` and `tel` addresses; any other link shows its
  text. Entities such as `&amp;` or `&#228;` show as their character
  (`&`, `ä`) in the text, never in a link's address. Everything else, HTML
  included, shows as you typed it.
- Leaving a document with unsaved changes (another document, Back, logging
  out, closing the tab) asks first.

Below the form are the file's name, type, size, pages and date added, the
downloads of the original and the archive PDF, and Retry when processing
failed.

The arrow keys (outside a field) or a swipe go to the previous or next
document of the list; Escape closes the document.

An admin can open and change other users' documents. Their tags,
correspondents and types are the owner's, and new ones created there
belong to the owner. Each change is in the audit log.

## Changing several documents at once

Each row has a checkbox. Shift-click a second checkbox to check every
document between the two; "Select all loaded" checks every document the
list has loaded, at most 500. On a phone the checkboxes appear after a long
press on a row.

Once documents are checked, a bar at the bottom changes them together:

| Action | What it does |
|---|---|
| Tags… | Adds and removes tags in one step. A tag cannot be both added and removed. |
| Correspondent…, Type… | Sets one, or removes it from the documents. |
| Remove from inbox, Move to inbox | Takes the documents out of the inbox, or puts them back. |
| Favorite, Unfavorite | Marks them as favourites, or unmarks them. |

A change is all or nothing: either every checked document changes or none
does. Afterwards the list and the counts load again, and the documents
stay checked for the next change.

When every loaded document is checked and the search has more, "Select all
matches of this search" covers every match, also those not loaded yet (at
most 5 000). Before such a change the bar counts the matches again and
asks with that number. The change then applies to exactly the documents
that were counted: if a document was added, changed or removed in the
meantime so that the matches are no longer the same, nothing is changed,
and the bar counts again and asks again ("The matches changed since they
were counted: now … documents.").

Tags, correspondents and types belong to one user. An admin who checked
documents of several users can move them in or out of the inbox, mark
them as favourites and remove a correspondent or type, but setting tags,
a correspondent or a type needs documents of a single user. The bar says
so before anything is sent: "Tags…" explains the rule, and
"Correspondent…" and "Type…" offer only removing.

The web UI cannot delete documents yet; it gets the trash with #12. The
API already moves a document to the trash (`DELETE
/api/documents/{id}`), where it stays for 30 days
(`PAPERLESS_GO_TRASH_RETENTION`) before it is removed for good: the trash
is `GET /api/documents?trashed=true`, and a document comes back with
`POST /api/documents/{id}/restore`. A document in the trash is in no list,
search or count, but its archive serial number stays taken.

## Tags, correspondents and types

"Tags, correspondents & types" at the foot of the sidebar lists them in
three tabs, each with the number of its documents. A click on a name opens
the list filtered by it.

- **Edit** renames a row. For a tag it also sets the colour (one of the
  palette, or any `#RRGGBB`) and the parent tag; "None (a top-level tag)" makes it a
  top-level tag. A tag cannot be moved under itself or one of its own
  sub-tags.
- On a larger screen a tag can also be dragged onto another tag to move it
  there, or onto the field above the tree to make it a top-level tag.
- **Delete** asks first and says how many documents are affected. The
  documents stay; they only lose the tag, correspondent or type. A deleted
  tag's sub-tags become top-level tags. If one of them has the name of an
  existing top-level tag, the delete is refused and names it: rename one
  of the two first.

An admin sees their own rows here, and everyone's with "Everyone's
documents" on, where each row of another user names the owner; an admin may rename, recolour, move and delete other users'
rows, and each such change is in the log.

## Admins

An admin sees their own documents, their own tags, correspondents and
types, and their own counts. "Everyone's documents" in the sidebar adds the
documents and names of all users; each row of another user then names the
owner. The switch is part of the page's address too, so it is off again
in a new window.
