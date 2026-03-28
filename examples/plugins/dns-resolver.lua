-- dns-resolver.lua
-- Example Nova plugin: custom DNS resolver for .nova domains.
--
-- Install: copy to ~/.nova/plugins/dns-resolver.lua
--
-- This plugin intercepts DNS resolution for hostnames ending in ".nova"
-- and maps them to IPs based on a local lookup table. Unmatched names
-- fall through to the default resolver.

local records = {
    ["api.nova"]     = "10.0.0.10",
    ["db.nova"]      = "10.0.0.20",
    ["cache.nova"]   = "10.0.0.30",
    ["monitor.nova"] = "10.0.0.40",
}

nova.register("dns_resolve", function(hostname)
    -- Only handle .nova domains.
    if not hostname:match("%.nova$") then
        return nil
    end

    local ip = records[hostname]
    if ip then
        nova.log("dns-resolver: " .. hostname .. " -> " .. ip)
        return ip
    end

    nova.log("dns-resolver: no record for " .. hostname)
    return nil
end)

nova.register("on_vm_start", function(vm_name)
    nova.log("dns-resolver: VM '" .. vm_name .. "' started, DNS records active")
end)

nova.log("dns-resolver plugin loaded with " .. tostring(#records) .. " records")
