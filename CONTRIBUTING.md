# Contribute

## Introduction

First, thank you for considering contributing to webhook! It's people like you that make the open source community such a great community! 😊

We welcome any type of contribution, not only code. You can help with

- **QA**: file bug reports, the more details you can give the better (e.g. screenshots with the console open)
- **Marketing**: writing blog posts, howto's, printing stickers, ...
- **Community**: presenting the project at meetups, organizing a dedicated meetup for the local community, ...
- **Code**: take a look at the [open issues](https://github.com/baragoon/webhook/issues). Even if you can't write code, commenting on them, showing that you care about a given issue matters. It helps us triage them.
- **Money**: we welcome financial contributions in full transparency on our [open collective](https://opencollective.com/webhook).

## Your First Contribution

Working on your first Pull Request? You can learn how from this *free* series, [How to Contribute to an Open Source Project on GitHub](https://egghead.io/series/how-to-contribute-to-an-open-source-project-on-github).

## Submitting code

Any code change should be submitted as a pull request. The description should explain what the code does and give steps to execute it. The pull request should also contain tests.

## Code review process

The bigger the pull request, the longer it will take to review and merge. Try to break down large pull requests in smaller chunks that are easier to review and merge.
It is also always helpful to have some context for your pull request. What was the purpose? Why does it matter to you?

## Release automation

Release Please uses a GitHub App token so checks on its release pull requests can run without manual approval. Repository administrators must:

1. [Create a GitHub App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app) with read and write repository permissions for Contents, Issues, and Pull requests.
2. Install the app on this repository and generate a private key.
3. Under Settings > Secrets and variables > Actions, add the app's client ID as the repository variable `APP_CLIENT_ID` and its private key as the repository secret `APP_PRIVATE_KEY`.

These settings must be in place before the release workflow runs. Existing approval-required runs still need manual approval.

## Financial contributions

We also welcome financial contributions in full transparency on our [open collective](https://opencollective.com/webhook).
Anyone can file an expense. If the expense makes sense for the development of the community, it will be "merged" in the ledger of our open collective by the core contributors and the person who filed the expense will be reimbursed.

## Questions

If you have any questions, create an [issue](https://github.com/baragoon/webhook/issues/new) (protip: do a quick search first to see if someone else didn't ask the same question before!).
You can also reach us at [hello@webhook.opencollective.com](mailto:hello@webhook.opencollective.com).

## Credits

### Contributors

Thank you to all the people who have already contributed to webhook!

[![Contributors](https://opencollective.com/webhook/contributors.svg?width=890)](https://github.com/baragoon/webhook/graphs/contributors)

### Backers

Thank you to all our backers! [[Become a backer](https://opencollective.com/webhook#backer)]

[![Backers](https://opencollective.com/webhook/backers.svg?width=890)](https://opencollective.com/webhook#backers)

### Sponsors

Thank you to all our sponsors! (please ask your company to also support this open source project by [becoming a sponsor](https://opencollective.com/webhook#sponsor))

[![Sponsor 0](https://opencollective.com/webhook/sponsor/0/avatar.svg)](https://opencollective.com/webhook/sponsor/0/website)
[![Sponsor 1](https://opencollective.com/webhook/sponsor/1/avatar.svg)](https://opencollective.com/webhook/sponsor/1/website)
[![Sponsor 2](https://opencollective.com/webhook/sponsor/2/avatar.svg)](https://opencollective.com/webhook/sponsor/2/website)
[![Sponsor 3](https://opencollective.com/webhook/sponsor/3/avatar.svg)](https://opencollective.com/webhook/sponsor/3/website)
[![Sponsor 4](https://opencollective.com/webhook/sponsor/4/avatar.svg)](https://opencollective.com/webhook/sponsor/4/website)
[![Sponsor 5](https://opencollective.com/webhook/sponsor/5/avatar.svg)](https://opencollective.com/webhook/sponsor/5/website)
[![Sponsor 6](https://opencollective.com/webhook/sponsor/6/avatar.svg)](https://opencollective.com/webhook/sponsor/6/website)
[![Sponsor 7](https://opencollective.com/webhook/sponsor/7/avatar.svg)](https://opencollective.com/webhook/sponsor/7/website)
[![Sponsor 8](https://opencollective.com/webhook/sponsor/8/avatar.svg)](https://opencollective.com/webhook/sponsor/8/website)
[![Sponsor 9](https://opencollective.com/webhook/sponsor/9/avatar.svg)](https://opencollective.com/webhook/sponsor/9/website)

<!-- This `CONTRIBUTING.md` is based on @nayafia's template https://github.com/nayafia/contributing-template -->