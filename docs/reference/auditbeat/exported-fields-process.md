---
mapped_pages:
  - https://www.elastic.co/guide/en/beats/auditbeat/current/exported-fields-process.html
---

% This file is generated! See scripts/generate_fields_docs.py

# Process fields [exported-fields-process]

Process metadata fields

**`process.exe`**
:   type: alias

alias to: process.executable


## owner [_owner]

Process owner information.

**`process.owner.id`**
:   Unique identifier of the user.

type: keyword


**`process.owner.name`**
:   Short name or login of the user.

type: keyword

example: albert


**`process.owner.name.text`**
:   type: text


