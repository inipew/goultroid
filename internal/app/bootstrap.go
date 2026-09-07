package app

// bootstrap.go is retained as a wiring index for backward compatibility.
// Actual construction logic is now split across:
//   - wiring_core.go      -> buildCore
//   - wiring_telegram.go  -> buildTelegramRuntime
//   - wiring_services.go  -> buildDomainServices
//   - catalog.go          -> buildPlugins / defaultPluginCatalog
//
// Keep this file small; do not add new wiring here.
