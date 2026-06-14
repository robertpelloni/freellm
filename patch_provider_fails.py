#!/usr/bin/env python3
"""Add providerAuthFails field to Gateway struct and initialize it."""

# Patch server.go - add field to struct
with open("internal/proxy/server.go", "r", encoding="utf-8") as f:
    content = f.read()

# Add providerAuthFails field after providerCooldown
old = "\tproviderCooldown map[string]time.Time // provider -> cooldown until"
new = (
    old
    + "\n\tproviderAuthFails map[string]int       // provider -> consecutive 401/402/403 failures"
)

if old in content:
    content = content.replace(old, new, 1)
    print("Added providerAuthFails field to Gateway struct")
else:
    print("ERROR: Could not find providerCooldown field")
    exit(1)

# Find the Gateway constructor/init and add initialization
# Look for "providerCooldown: make(" pattern
old_init = "providerCooldown: make(map[string]time.Time),"
new_init = old_init + "\n\t\tproviderAuthFails: make(map[string]int),"

if old_init in content:
    content = content.replace(old_init, new_init, 1)
    print("Added providerAuthFails initialization")
else:
    print(
        "WARNING: Could not find providerCooldown initialization, searching for make(map..."
    )
    # Try alternate init pattern
    old_init2 = "providerCooldown: make(map[string]time.Time)"
    new_init2 = old_init2 + ",\n\t\tproviderAuthFails: make(map[string]int)"
    if old_init2 in content:
        content = content.replace(old_init2, new_init2, 1)
        print("Added providerAuthFails initialization (alt pattern)")
    else:
        # Find where providerCooldown is initialized
        for i, line in enumerate(content.split("\n"), 1):
            if "providerCooldown" in line and "make" in line:
                print(f"Found providerCooldown init at line {i}: {line.strip()}")
        print("ERROR: Could not add initialization")

# Add tracking functions at the end of the file
tracking_funcs = """

// recordProviderAuthFail tracks consecutive 401/402/403 failures per provider.
// Returns true if the provider should be considered dead (3+ consecutive auth failures).
func (g *Gateway) recordProviderAuthFail(provider string) bool {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	g.providerAuthFails[provider]++
	fails := g.providerAuthFails[provider]
	if fails >= 3 {
		log.Printf("[ROUTER] Provider %s marked as dead (%d consecutive auth failures)", provider, fails)
		return true
	}
	return false
}

// resetProviderAuthFails clears the auth failure counter on a successful request.
func (g *Gateway) resetProviderAuthFails(provider string) {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	if g.providerAuthFails[provider] > 0 {
		g.providerAuthFails[provider] = 0
	}
}

// isProviderDead returns true if a provider has 3+ consecutive auth failures.
func (g *Gateway) isProviderDead(provider string) bool {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	return g.providerAuthFails[provider] >= 3
}
"""

content += tracking_funcs
print("Added tracking functions")

with open("internal/proxy/server.go", "w", encoding="utf-8") as f:
    f.write(content)

print("Done patching server.go")
