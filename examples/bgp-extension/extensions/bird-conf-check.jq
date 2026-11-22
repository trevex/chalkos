# The config bird-conf's check parses: a constant for each password bird.conf names, which the
# node defines from its credentials, then bird.conf itself, which $conf names.
([scan("'secret_[^']*'")] | unique[] | "define \(.) = \"checked\";"),
"include \"\($conf)\";"
