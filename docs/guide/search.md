# Search

paperless-go searches the title, the recognized text, the notes, the
original file name and the archive serial number (ASN) of your documents,
and the names of their correspondent, document type and tags. Words in
German and English are found in their inflected forms too. Filters narrow a
search to tags, correspondents, types, dates and status flags.

You only ever find your own documents. An admin searches their own
documents too, and all users' documents with "Everyone's documents" in the
sidebar (see [Documents](./documents)); through the API an admin searches
all users' documents.

Type a search into the field above the document list and press Enter. The
results replace the list; "Load more" shows the next 50, and clearing the
field brings the whole list back. An entry chosen in the sidebar (a tag,
the inbox) narrows the search further. Each search you type is recorded in
your [audit log](./audit-log), with the text you searched for; choosing
an entry in the sidebar is browsing and is not recorded. Scripts search with `GET /api/documents?q=…` and an API key with the
`read` scope (see [Architecture](../development/architecture#documents-api)
for the parameters).

## Words and phrases

| You type | It finds documents |
|---|---|
| `Rechnung` | with the word in any field. Also inflected forms (`Rechnungen`), the word as typed (`RE-2024-00123`, an IBAN), words that start with it when it has 3 or more characters (`Rechn` finds `Rechnung`), and documents whose tag, correspondent or type name contains it (`versich` finds the tag "Kfz-Versicherung"; a tag also finds the documents of its sub-tags). |
| `Telekom Rechnung` | with both words. Each may be in a different field: the correspondent "Telekom" and the word "Rechnung" in the text. |
| `"Stadtwerke senden"` | with the words next to each other, in this order. |
| `-Mahnung`, `-"zweite Mahnung"` | without the word or phrase. |
| `Strom OR Gas` | with either word. `OR` is written in capitals and stands between two words or phrases; everything else is joined by AND. There are no parentheses. |

Case and accents do not matter: `cafe` finds `Café`, `strasse` finds
`Straße`, and `Gebuhren` finds `Gebühren`. From PostgreSQL 18 on, which the
Compose setup uses, `Gebuehren` finds `Gebühren` too.

Some limits of the word match:

- **Compound words.** A word inside a German compound is not found:
  `rechnung` does not find `Stromrechnung`. The start of a compound is:
  `Strom` finds it as a prefix.
- **Short words.** A word of one or two characters matches only whole
  words and whole names, never as a prefix or part of a name.
- **Phrases in long texts.** A phrase is found within roughly the first
  16 000 words of a document's text. Further on, only single words are
  found.
- **Very long texts.** The search covers the first 300 000 characters of a
  document's text (less if the text has very many different words).

## Filters

| You type | It finds documents |
|---|---|
| `tag:Steuer` | tagged "Steuer" or one of its sub-tags. The name must match exactly, ignoring case. |
| `tag:Steuer,Auto` | with any of these tags. |
| `tag:Steuer tag:Auto` | with both tags. |
| `-tag:Steuer` | without the tag and its sub-tags. |
| `tag:"Kfz Versicherung"` | A name with spaces or commas is quoted. |
| `correspondent:Telekom`, `type:Rechnung` | The same rules, for the correspondent and the document type. `-correspondent:Telekom` also finds documents without a correspondent. |
| `asn:123`, `asn:123,124` | with this archive serial number. |
| `date:2024` | dated in 2024: the document's own date, such as the invoice date. `created:` means the same. |
| `date:2024-03`, `date:2024-03-15` | dated in that month, or on that day. |
| `date:2024-01-01..2024-06-30` | dated from the first to the last day, both included. Either end may be left out: `date:..2023`, `date:2024-07..`. |
| `date:>2024-03-15`, `date:>=2024-03`, `date:<2024`, `date:<=2024-06` | dated after, from, before or up to that day, month or year. |
| `added:…` | The same forms, for the day the document was added to paperless-go. |
| `is:inbox`, `is:favorite` | in the inbox, or marked as favorite. `-is:inbox` finds the documents not in the inbox. |

Days begin and end at midnight in your browser's time zone.

A filter with a misspelled name, or with a value that is not valid, is
searched as a word: `12:30`, `https://example.com` and `date:yesterday`
are plain text. A tag, correspondent or type name that does not exist finds
no documents. `-` before any filter excludes what it matches.

A search has at most 1000 characters and 32 words, phrases and filter
values.

## Order

A search with words lists the best matches first: a word in the title
counts most, then the tags, correspondent and type, then the notes and file
name, then the text. Without words, or with only excluded words (`-Mahnung`), the newest added
documents come first.
The sort menu next to the list's heading (or `sort` and `order` through the
API) sorts a search by the date added or by the document's date instead,
newest or oldest first.

Each result shows up to two passages of the text around the words it
matched.
