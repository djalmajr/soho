---
version: <version>
name: <name>
description: <description>
colors:
  <token-name>: <css color>
typography:
  <token-name>:
    fontFamily: <family>
    fontSize: <size>
rounded:
  <scale-level>: <dimension>
spacing:
  <scale-level>: <dimension>
# optional — omit the block when the project has no component tokens:
components:
  <component-name>:
    <property>: <value or {token reference}>
---

The `designer` fills this file reading the project's real code — the theme files, the token files, the components — and never copies this form: a delivered `DESIGN.md` leaves no `<...>` behind. The tokens in the front matter are the normative values; the prose applies them. A section that does not apply carries the one line `Not applicable: <reason>`, and a front matter key that does not apply is omitted. The model works with or without a component framework: without one, `components` is omitted and the component sections say so.

## Overview

<one short paragraph: what the identity is and what it prioritizes, or the line Not applicable: <reason>>

## Colors

<one line per color token: its role in the interface, or the line Not applicable: <reason>>

## Typography

<one line per type token: where the step is used, or the line Not applicable: <reason>>

## Layout

<the grid, the density and the breakpoints the layout follows, or the line Not applicable: <reason>>

## Elevation & Depth

<the shadows and the depth levels, or the line Not applicable: <reason>>

## Shapes

<the radii and the borders, or the line Not applicable: <reason>>

## Components

<one line per component with a pointer to its spec file under design/, or the line Not applicable: <reason>>

## Do's and Don'ts

<the short list of what the interface must and must not do, or the line Not applicable: <reason>>

## Token layers

<where each value comes from, in three layers; a project token points at its origin and never rewrites the origin's value in silence>

- origin: <the upstream source a token ships with: what it is and where its value lives>
- project alias: <the project's override, when it exists: the value, the file that declares it, and the origin token it points at>
- component: <the component-level usage, when it exists: which component takes which token>

## Specs

<when a component or pattern needs more than a line, its spec lives in design/<name>.md, read only when that part is touched; document only what exists, and keep the root short>
