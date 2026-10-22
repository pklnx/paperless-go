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

## Admins

An admin sees their own documents, their own tags, correspondents and
types, and their own counts. "Everyone's documents" in the sidebar adds the
documents and names of all users; each row of another user then names the
owner. The switch is part of the page's address too, so it is off again
in a new window.
