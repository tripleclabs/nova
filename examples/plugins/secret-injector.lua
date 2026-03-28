-- secret-injector.lua
-- Example Nova plugin: injects secrets from environment variables into VMs.
--
-- Install: copy to ~/.nova/plugins/secret-injector.lua
--
-- On VM start, this plugin logs which secrets would be injected.
-- In a real implementation, it would write secrets to the cloud-init
-- config or a mounted volume.

local secrets = {
    "DATABASE_URL",
    "API_KEY",
    "JWT_SECRET",
}

nova.register("on_vm_start", function(vm_name)
    nova.log("secret-injector: preparing secrets for VM '" .. vm_name .. "'")
    for _, name in ipairs(secrets) do
        -- In a real plugin, nova.get_secret(name) would fetch from a vault.
        nova.log("secret-injector: would inject " .. name .. " into " .. vm_name)
    end
end)

nova.register("on_snapshot", function(snap_name)
    nova.log("secret-injector: WARNING - snapshot '" .. snap_name .. "' may contain secrets!")
end)

nova.log("secret-injector plugin loaded")
