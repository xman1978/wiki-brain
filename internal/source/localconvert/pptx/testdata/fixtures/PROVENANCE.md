# Test fixture provenance

All committed `.pptx` fixtures are license-safe and contain no personal or
confidential content.

## Synthetic (repo-authored)

These are minimal OOXML zips authored for this project's tests. No third-party
license applies.

| File | Description |
|------|-------------|
| `basic.pptx` | 2 slides: title, bullets, one image, speaker notes. |
| `table.pptx` | A slide containing a table. |

## Third-party (python-pptx test corpus — MIT)

Downloaded from [python-pptx](https://github.com/scanny/python-pptx)
(The MIT License, Copyright (c) 2013 Steve Canny), pinned to commit
`278b47b1dedd5b46ee84c286e77cdfb0bf4594be`.

| File (here) | Upstream path | sha256 |
|-------------|---------------|--------|
| `real-simple.pptx` | `tests/test_files/test.pptx` | `8765677cdf43181ef41657cedf28485b5f2cbf166667218c217af07f8336c96f` |
| `real-rich.pptx` | `features/steps/test_files/ph-populated-placeholders.pptx` | `b08599276b4d708757951f2d45ac74cb4e81e94463d575e4d857690d0d231a60` |

`real-simple.pptx` is a 1-slide deck (centre-title + subtitle placeholders).
`real-rich.pptx` is a 9-slide deck exercising image placeholders, a table, and
populated body placeholders — a real PowerPoint-authored file (full layout/master
inheritance, theme parts), unlike the hand-built synthetic fixtures.
