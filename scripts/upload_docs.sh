#!/bin/bash

cd docs/build
mv html llm-bridge
tar -czf docs.tar.gz llm-bridge

curl -F 'file=@docs.tar.gz' https://docs.poraodojuca.dev/e/ -H "Authorization: Key $TUPI_AUTH_KEY"
