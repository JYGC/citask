# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository. It covers the engineering standards that apply to all work here.

## This repository is public

Never commit real hostnames, addresses, account details, tokens or a real `citask.json`. Specs in this repository are public too, so the same rule applies to them. Use placeholders such as `example.com` in tests and documentation.

Nothing project-specific belongs in the source. A project joins through its own `ci-steps.json` and one entry in `citask.json`.

## Layered Architecture

All code must follow the layered architecture pattern with clear separation of concerns. Each layer may only depend on the layer directly below it.

| Layer | Responsibility |
|---|---|
| **API** | HTTP handlers, request/response mapping, input validation |
| **Application** | Use-case orchestration; coordinates services without business logic |
| **Service** | Business logic and domain rules |
| **Repository** | Data access abstraction; hides persistence details from services |
| **Store** | Persistence (database queries, external API calls) |

Optional layers: **Domain** (pure entities and value objects, no dependencies), **DTO/Schema** (data transfer objects at layer boundaries).

In this repository the API layer is the command line in `cmd/citask`.

### References

1. [Microsoft Learn — Infrastructure Persistence Layer Design](https://learn.microsoft.com/en-us/dotnet/architecture/microservices/microservice-ddd-cqrs-patterns/infrastructure-persistence-layer-design)
2. [From Request to Database: Three-Layer Architecture in API Development](https://konstantinmb.medium.com/from-request-to-database-understanding-the-three-layer-architecture-in-api-development-1c44c973c7af)
3. [Repository Pattern with Layered Architecture](https://medium.com/@leadcoder/repository-pattern-with-layered-architecture-35f7b9182ebf)
4. [Layered Architecture Template for REST APIs (Java/Spring Boot)](https://kamilmazurek.pl/layered-architecture-template)
5. [Service–Repository Pattern in Action](https://medium.com/@albinaji.official/service-repository-pattern-in-action-0db4bb9a474b)
6. [Repository and Services Pattern in a Multilayered Architecture](https://www.vodovnik.com/repository-and-services-pattern-in-a-multilayered-architecture/)
7. [Clean Architecture — Incorporating Repository Pattern](https://medium.com/@bert.oneill/clean-architecture-incorporating-repository-pattern-388742e0b54e)
8. [Unpacking Clean Architecture Layers — Domain, Application, Infrastructure Services](https://www.dandoescode.com/blog/unpacking-the-layers-of-clean-architecture-domain-application-and-infrastructure-services)
9. [Clean Architecture: Understanding the Infrastructure and Persistence Layers](https://dev.to/moh_moh701/what-is-clean-architecture-understanding-the-infrastructure-and-persistence-layers-2pca)
10. [clean-architecture-api-boilerplate (GitHub)](https://github.com/luizomf/clean-architecture-api-boilerplate/blob/master/README.md)
11. [Building a Layered Architecture in NestJS & TypeScript](https://medium.com/@patrick.cunha336/building-a-layered-architecture-in-nestjs-typescript-repository-pattern-dtos-and-validators-08907a8ac4cb) — **⚠️ Uses NestJS/Node.js. Apply the structural concepts only, not the framework specifics.**
12. [Clean Architecture Design Guide for Backend API Developments](https://naskay.com/blog/clean-architecture-design-guide-for-backend-api-developments/)

## Development rules

- Always read a file before editing it.
- No speculative abstractions — only build what is needed now.
- **Use long, descriptive names instead of comments.** Every nameable thing — variable, function, method, class, module, file, test — must state what it is for in its own name, with no comment alongside it. Always choose the long, explicit name over a short name plus an explanatory comment; name length is not a cost here. Write the comment only when the name that would replace it runs longer than 200 words. Where a comment would introduce a block of code, extract that block into a named function and let the name do the explaining.
- **Comments are for what no name can express** — why a non-obvious approach was chosen, an external constraint or workaround, or a pointer to the spec or issue behind the code. Never a restatement of what the code does.
- Format Go code with `gofmt` and `goimports` before committing.
- Code style: [Google Go style guide](https://google.github.io/styleguide/go/guide).
- Use only the Go standard library unless a change agrees otherwise.

## Testing

### Mandate

**Unit tests must be written before the implementation code they cover.** Write the test, watch it fail, then write the minimum code to make it pass.

Go will not compile a call to code that does not exist, so the first failure of a new test is a compile error. Do not soften a test to get past it.

### Running tests

Run `go test ./...`. OpenBSD is the platform `citask` targets, so a change is finished only when the tests pass there.

Automated tests must never call the leftover process stopper for real. They inject a recording fake.
