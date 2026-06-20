# Security Policy

## Supported versions

Security fixes are released against the latest published version of the provider on
the [Terraform Registry](https://registry.terraform.io/providers/prodata-cloud/prodata).
Please upgrade to the latest version before reporting an issue.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security problems.

Report suspected vulnerabilities privately through the ProData Cloud support channels:

- Help Desk: https://helpdesk.pro-data.tech
- GitHub: use **Security → Report a vulnerability** (private advisory) on this repository.

Please include enough detail to reproduce the issue (provider version, Terraform version,
a minimal configuration, and the observed vs. expected behavior). We will acknowledge your
report and keep you updated on remediation.

## Handling credentials

This provider reads API credentials from configuration or the `PRODATA_API_KEY_ID` /
`PRODATA_API_SECRET_KEY` environment variables. Never commit credentials or paste them into
issues or pull requests. The provider marks `api_secret_key` (and credential-bearing
attributes such as `kube_config`) as sensitive so Terraform redacts them in plan and apply
output; if you ever see a secret rendered in plain text, treat it as a vulnerability and
report it privately.
