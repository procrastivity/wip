(.services | keys) == ["authority-env", "client-env"] and
(.volumes | keys) == ["authority-state", "client-state"] and
(.networks | keys) == ["lab"] and
(.services["authority-env"].networks | keys) == ["lab"] and
(.services["client-env"].networks | keys) == ["lab"] and
(.services["authority-env"].environment.LAB_ENVIRONMENT == "authority") and
(.services["client-env"].environment.LAB_ENVIRONMENT == "client") and
(.services["authority-env"].volumes | length) == 1 and
(.services["client-env"].volumes | length) == 1 and
(.services["authority-env"].volumes[0].type == "volume") and
(.services["client-env"].volumes[0].type == "volume") and
(.services["authority-env"].volumes[0].source == "authority-state") and
(.services["client-env"].volumes[0].source == "client-state") and
(.services["authority-env"].volumes[0].target == "/var/lib/online-authority-proof") and
(.services["client-env"].volumes[0].target == "/var/lib/online-authority-proof") and
all(.services[];
  (.privileged // false) == false and
  (.network_mode // "") != "host" and
  (.pid // "") != "host" and
  (.ipc // "") != "host" and
  (.read_only // false) == true and
  ((.cap_add // []) | length) == 0 and
  ((.cap_drop // []) | index("ALL")) != null and
  ((.security_opt // []) | index("no-new-privileges:true")) != null and
  ((.devices // []) | length) == 0 and
  ((.ports // []) | length) == 0 and
  all(.volumes[]?; .type == "volume") and
  all((.environment // {}) | to_entries[]?;
    .key == "LAB_ENVIRONMENT")) and
all(.networks[]; (.internal // false) == true)
