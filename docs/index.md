---
layout: home

hero:
  name: paperless-go
  text: Your documents on your own server
  image:
    src: /logo.svg
    alt: paperless-go logo
  tagline: Scan, recognize, tag and find your documents, in the style of paperless-ngx. A server version of the Document Archive app for iPhone, iPad and Mac.
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Roadmap
      link: /development/roadmap

features:
  - title: Plain files
    details: Documents are stored as PDF files on disk, metadata and the search index in PostgreSQL.
  - title: Text recognition
    details: Scans and image-only PDFs get a text layer with OCRmyPDF and Tesseract (German and English), on your server. Mistral OCR is optional.
  - title: Correspondents, types, tags
    details: The same data model as the Document Archive app, with matching rules for automatic assignment.
  - title: Runs anywhere Docker runs
    details: Docker Compose setup with images for amd64 and arm64, so it runs natively on Apple Silicon Macs.
---

::: warning Early development
This version is the foundation: the server, the database schema handling and
the web UI shell. Documents, OCR, search and login follow step by step; see
the [roadmap](/development/roadmap).
:::

::: warning Entirely vibe-coded
All code, configuration and documentation are written by an AI (Claude) under
human direction, without line-by-line human review. Review it yourself before
trusting it with important documents, and keep independent backups.
:::
