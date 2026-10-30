# Project: Matrix server compatible implementation

## Infra

golang, sqlite (see skills.md's for more details)

## Idea

to make a basic drop-in replacement for https://spec.matrix.org/latest/
each functionality will be added as a functional test as well in a sperate test directory (instead of adding each test file next to the golang implementation file) because we want to run a spearate 'compatibility' test suite, this will make development easier while progressing (easy lookup which endpoint is already supported)

## Endpoints

provided is an md (SPECS.md) with all endpoints that we need to support

## Way of working

implement an endpoint, add a functional test, try if it works like it should, move to the next..

## Holistic

after a while another more holistic user-script test can also be added to walk through each step (creating account, joining a channel, receive message, upload file etc...).

## Phases

Lets split the work up in phases, like phase 1 bare minimal POC, phase 2 add most used endpoints, phase 3 ...
