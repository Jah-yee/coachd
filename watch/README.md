# coachd watch app

A Connect IQ watch app for fēnix and Forerunner watches (the models in `manifest.xml`). It pairs with your coachd once, then copies the workouts you approved onto the watch and tells coachd it has them. It only reads coachd's device API (`/device/v1/*`): it cannot approve, change or withdraw anything.

Its documentation is on the documentation site:

- [Connect IQ Store](https://apps.garmin.com/apps/14f05551-a935-44d7-9227-6b4762e4b341): install it from here.
- [Watch app](https://stefiix92.github.io/coachd/docs/watch/): server setup and installing it.
- [Pairing](https://stefiix92.github.io/coachd/docs/watch/pairing/), [using the app](https://stefiix92.github.io/coachd/docs/watch/using/) and [status messages](https://stefiix92.github.io/coachd/docs/watch/status-messages/).
- [Development](https://stefiix92.github.io/coachd/docs/watch/development/): the SDK, `build.sh`, sideloading and the simulator tests.

The Store listing and upload checklist are in [`STORE.md`](STORE.md).
