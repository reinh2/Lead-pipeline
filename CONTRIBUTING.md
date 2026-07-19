# Contributing

Thanks for improving this demo case study. Keep changes focused, avoid adding
real credentials or personal data, and document any user-visible behaviour.

## Local workflow

1. Create `.env` from `.env.example` only when you need local configuration.
   Never commit it or service-account keys.
2. Make the smallest change that solves the issue and add or update regression
   tests when behaviour changes.
3. From the project root, run:

   ```bash
   make fmt
   make check
   ```

4. For a manual demo, run `make up` and submit a test lead with
   `make demo-lead`.

## Pull requests

Describe the problem, the chosen solution, and how you verified it. Do not
include API tokens, CSV lead data, screenshots with personal data, or claims
that the demo is production-ready.
