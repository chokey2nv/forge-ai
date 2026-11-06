# store

`store.go` declares one repository interface per collection, and `mongo/` implements them, one file each. Handlers depend on the interfaces, so tests use small in-memory fakes.

A new method needs adding to the interface and the Mongo version; a fake that embeds the interface keeps compiling, then panics if the method is called.

`mongo/secrets.go` keeps the generated callback secret when `FORGE_CALLBACK_SECRET` isn't set.
