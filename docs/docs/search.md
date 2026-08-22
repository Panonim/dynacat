# Search

This documentation file describes the search functionality available in the DynGlance Docs. 
It explains optional operators that can be used for more precise information retrieval.

## Operators

### `$in:<page>`

Search only within a specific document page.

Examples:

- `$in:configuration`
- `cache $in:configuration`
- `$in:docker-options`

### `$has:<type>`

Require a matching line to have one of these nearby:

- `link`
- `code`
- `image`

Examples:

- `$has:code`
- `cache $has:link`
- `$has:link/code/image`

Multiple `$has` values are treated as "any of these".

## Combine operators

You can combine both:

`token $in:configuration $has:code`
