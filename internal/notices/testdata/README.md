# testdata

`notices.golden.json` is a verbatim copy of the site's golden fixture:

- Source: `obentoo.org/coming-soon/tests/fixtures/notices.golden.json`
  (local checkout: `/home/otaku/Projetos/Pessoais/Bentoo/obentoo.org/coming-soon/tests/fixtures/notices.golden.json`)
- Commit: `d0398a2` — "feat(002): publish the notices as JSON Feed and Atom"

Do not edit it here. When the site's fixture changes, copy it again and update
the commit above: `TestParseFeed_AcceptsTheGoldenFixture` is what catches drift
between the site's validation and `notices.ParseFeed`.
