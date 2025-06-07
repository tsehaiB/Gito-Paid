GPTChat
=======

GPTChat is a client which gives GPT-4 some unique tools to be a better AI.

With GPTChat, GPT-4 can:
* remember useful information and recall it later
* recall information without knowing it's previously remembered it
* write it's own plugins and call them
* decide to write plugins without being prompted
* complete tasks by combining memories and plugins
* use multi-step commands to complete complex tasks

### Getting started

You'll need:

* A working installation of Go (which you download from https://go.dev/)
* An OpenAI account
* An API key with access to the GPT-4 API

If you don't have an API key, you can get one here:
https://platform.openai.com/account/api-keys

If you haven't joined the GPT-4 API waitlist, you can do that here:
https://openai.com/waitlist/gpt-4-api

Once you're ready:

1. Set the `OPENAI_API_KEY` environment variable to avoid the API key prompt on startup
2. Run GPTChat with `go run .` from the `gptchat` directory
3. Have fun!

## Memory

GPT-4's context window is pretty small.

GPTChat adds a long term memory which GPT-4 can use to remember useful information.

For example, if you tell GPT-4 what pets you have, it'll remember and can recall that information to answer questions even when the context is gone.

[See a GPT-4 memory demo on YouTube](https://www.youtube.com/watch?v=PUFZdM1nSTI)

## Plugins

GPT-4 can write its own plugins to improve itself.

For example, GPT-4 is pretty bad at math and generating random numbers.

With the plugin system, you can ask GPT-4 to generate two random numbers and add them together, and it'll write a plugin to do just that.

[See a GPT-4 plugin demo on YouTube](https://www.youtube.com/watch?v=o7M-XH6tMhc)

ℹ️ Plugins are only supported on unix based systems like Linux and MacOS - to get plugins working on Windows, you'll need to use something like WSL2.

## Contributing

PRs to add new features are welcome.

Be careful of prompt changes - small changes can disrupt GPT-4's ability to use the commands correctly.

## Disclaimer

You should supervise GPT-4's activity.

In one experiment, GPT-4 gave itself internet access with a HTTP client plugin - this seemed like a bad idea.

### Supervised mode

GPTChat will run in supervised mode by default.

This doesn't restrict any functionality, but does require user confirmation before compiling and executing any plugin code written by GPT, giving users a chance to review the code for safety before executing it.

⚠️ Code written by GPT is untrusted code from the internet and potentially dangerous

All code is compiled and executed as your user, with the same level of permissions your user has.  It may be safer to run this in a container or virtual machine.

Supervised mode can be disabled but I wouldn't recommend it.

# License

See [LICENSE.md](LICENSE.md) for more information.

Copyright (c) 2023 Ian Kent