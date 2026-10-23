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
| Trash | Deleted documents, until they are removed for good (see [Trash](#trash)). |
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

## Saved views

A saved view keeps a search, a filter and a sort under a name, in the
sidebar's "Saved views" section.

- **Save view…** next to the sort menu saves what the list shows now: the
  search, the sidebar's filter, the sort and its direction, and for an
  admin "Everyone's documents". It is there while a search, a filter or a
  sort is chosen, not in the trash.
- A click on a view opens the list with its search, filter and sort; the
  view is marked as long as the list shows them.
- The **⋯** next to a view renames it, sets it to what the list shows now
  ("Update to the current filter"), moves it up or down, or deletes it
  (asked first; only the view is deleted, never documents). The views keep
  the order you give them, not an alphabetical one.

Saved views are your own: nobody else sees them, admins neither. Names may
repeat. Views made in the Document Archive app use the app's own format
and are not shown here yet; importing them comes with the archive
import. A view saved here keeps the app's settings it already had.

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
| Delete… | Moves them to the trash, after asking with their number. |

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

## Trash

**Delete** in a row, in a document's form or in the bar of checked
documents moves documents to the trash. Deleting one document asks
nothing: a notice "… moved to the trash" offers **Undo** for 8 seconds,
also when another page is opened meanwhile; the keyboard focus moves to it,
and its time stands still while it has the focus or the pointer.
Deleting checked documents, or every match of a search, asks first with
their number.

A document in the trash is in no list, search or count, and it cannot be
changed. It stays there for 30 days (the admin can change that with
`PAPERLESS_GO_TRASH_RETENTION`) and is then removed for good, files
included. Its archive serial number stays taken meanwhile; the message
of a taken number then says the other document is in the trash.

**Trash** in the sidebar, below Favorites, lists the trash with its
number, newest deleted first; each row says when the document was deleted
and when it will be removed. The search field searches the trash with the
same syntax as everywhere; the sidebar's other entries leave the trash.
The upload panel is not shown there (a file dropped on the window opens
the documents again).

- **Restore** brings a document back as it was: its tags, correspondent,
  type, archive serial number, inbox and favourite.
- **Delete permanently** removes a document and its files for good. It
  always asks first, and it cannot be undone.
- **Empty trash** (shown while your own trash holds documents) counts your trash, asks "Remove N documents
  permanently?" and then removes exactly those. If the trash changed in
  between, nothing is removed and it counts and asks again. At most 5 000
  documents go at once; for a larger trash, search it to narrow it down,
  select all matches and delete them permanently.
- Checked documents of the trash can be restored or deleted permanently
  together; deleting permanently asks with the number.

Opened from the trash, a document shows "In the trash since …" with
Restore and Delete permanently; its form is read-only until it is
restored. Previous and next walk the trash. If the document was moved to
the trash elsewhere while you edited it, saving is refused, your changes
stay in the form, and after Restore you can save them.

Uploading a file that is already there as a document in the trash says
"already there, in the trash" with a **Restore** button; nothing is
restored by itself. (The consume folder, `./pgo import` and upload-only API
keys, where nobody sees that button, restore the document by themselves.)
While such a document is being removed for good, the upload is refused
for about a minute; the row says when to try again and offers "Try again".

An admin's "Empty trash" (with "Everyone's documents" on: "Empty my
trash") removes only the admin's own trash. Other users' trashed documents
are removed one by one or by checking them; the confirmation names their
owners. Deleting every match of a search permanently works only on the
admin's own trash, with "Everyone's documents" off.

Deleting a tag, correspondent or type counts only documents outside the
trash; documents in the trash lose it too.

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
- **Delete** asks first and says how many documents are affected (not
  counting the trash, whose documents lose it too). The
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
